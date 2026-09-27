package agent

import (
	"bufio"
	"bytes"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/vsemenov/ship/internal/proto"
)

// All apps on a server share one Postgres container; each app gets its own
// database and role. It is reachable only from containers on the ship network.
const (
	shipNetwork  = "ship"
	pgContainer  = "ship-postgres"
	pgImage      = "postgres:17-alpine"
	pgVolume     = "ship-postgres-data"
	pgAdminFile  = stateDir + "/postgres.json"
	backupsDir   = stateDir + "/backups"
	keepBackups  = 7
	backupUnit   = "ship-backup"
	systemdDir   = "/etc/systemd/system"
	releaseLimit = 10 * time.Minute
)

func ensureNetwork() error {
	if exec.Command("docker", "network", "inspect", shipNetwork).Run() == nil {
		return nil
	}
	if out, err := combined("docker", "network", "create", shipNetwork); err != nil && !strings.Contains(out, "already exists") {
		return &proto.Error{Code: proto.CodeContainer, Message: "cannot create docker network " + shipNetwork, Logs: tail(out, 5)}
	}
	return nil
}

func randHex(n int) string {
	b := make([]byte, n)
	rand.Read(b)
	return hex.EncodeToString(b)
}

func dbFile(app string) string { return filepath.Join(appDir(app), "db.json") }

func loadDB(app string) (*proto.Database, error) {
	b, err := os.ReadFile(dbFile(app))
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var d proto.Database
	return &d, json.Unmarshal(b, &d)
}

func ensurePostgres() error {
	if err := ensureNetwork(); err != nil {
		return err
	}
	if st, err := inspectContainer(pgContainer); err == nil {
		if st.Status != "running" {
			progress("Starting shared Postgres")
			if out, err := combined("docker", "start", pgContainer); err != nil {
				return &proto.Error{Code: proto.CodeDatabase, Message: "cannot start " + pgContainer, Logs: tail(out, 10)}
			}
		}
	} else {
		progress("Creating shared Postgres (%s)", pgImage)
		pw := randHex(24)
		if err := writeJSON(pgAdminFile, map[string]string{"user": "postgres", "password": pw}); err != nil {
			return err
		}
		out, err := combined("docker", "run", "-d",
			"--name", pgContainer,
			"--restart", "unless-stopped",
			"--network", shipNetwork,
			"--label", "ship.service=postgres",
			"-v", pgVolume+":/var/lib/postgresql/data",
			"-e", "POSTGRES_PASSWORD="+pw,
			"--log-opt", "max-size=10m", "--log-opt", "max-file=3",
			pgImage, "-c", "shared_buffers=64MB", "-c", "max_connections=100")
		if err != nil {
			return &proto.Error{Code: proto.CodeDatabase, Message: "cannot create " + pgContainer, Logs: tail(out, 10)}
		}
	}
	deadline := time.Now().Add(90 * time.Second)
	for time.Now().Before(deadline) {
		if exec.Command("docker", "exec", pgContainer, "pg_isready", "-U", "postgres", "-q").Run() == nil {
			return nil
		}
		time.Sleep(time.Second)
	}
	return &proto.Error{Code: proto.CodeDatabase, Message: "shared Postgres did not become ready", Logs: containerLogs(pgContainer, 20)}
}

// psql runs SQL as the postgres superuser over the container's local socket.
func psql(sql string) (string, error) {
	cmd := exec.Command("docker", "exec", "-i", pgContainer, "psql", "-U", "postgres", "-v", "ON_ERROR_STOP=1", "-qAt")
	cmd.Stdin = strings.NewReader(sql)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return "", &proto.Error{Code: proto.CodeDatabase, Message: "postgres query failed", Logs: tail(string(out), 10)}
	}
	return strings.TrimSpace(string(out)), nil
}

func cmdDB(args []string) (any, error) {
	if len(args) == 0 {
		return nil, proto.Errf(proto.CodeUsage, "", "usage: db add|info|shell|backup|restore|backup-all --app NAME")
	}
	op := args[0]
	fs := newFlags("db " + op)
	name := fs.String("app", "", "")
	sql := fs.String("c", "", "")
	if err := parse(fs, args[1:]); err != nil {
		return nil, err
	}
	if op == "backup-all" {
		return backupAll()
	}
	if err := checkAppName(*name); err != nil {
		return nil, err
	}
	switch op {
	case "add":
		return dbAdd(*name)
	case "info":
		d, err := loadDB(*name)
		if err != nil {
			return nil, err
		}
		if d == nil {
			return nil, proto.Errf(proto.CodeNotFound, "create one with `ship db add`", "app %q has no database", *name)
		}
		return map[string]any{"database": d, "backups": listBackups(*name)}, nil
	case "shell":
		return nil, dbShell(*name, *sql)
	case "backup":
		d, err := requireDB(*name)
		if err != nil {
			return nil, err
		}
		return backupDB(*name, d)
	case "restore":
		return dbRestore(*name)
	}
	return nil, proto.Errf(proto.CodeUsage, "", "unknown db operation %q", op)
}

func requireDB(app string) (*proto.Database, error) {
	d, err := loadDB(app)
	if err != nil {
		return nil, err
	}
	if d == nil {
		return nil, proto.Errf(proto.CodeNotFound, "create one with `ship db add`", "app %q has no database", app)
	}
	return d, nil
}

func dbAdd(app string) (any, error) {
	unlock, err := lockApp(app)
	if err != nil {
		return nil, err
	}
	defer unlock()
	if d, err := loadDB(app); err != nil || d != nil {
		return map[string]any{"database": d, "created": false}, err
	}
	if err := ensurePostgres(); err != nil {
		return nil, err
	}
	// app names are [a-z0-9-], so this is a safe identifier once dashes are replaced
	ident := strings.ReplaceAll(app, "-", "_")
	if n, err := psql(fmt.Sprintf("SELECT count(*) FROM pg_database WHERE datname = '%s';", ident)); err != nil {
		return nil, err
	} else if n != "0" {
		return nil, proto.Errf(proto.CodeDatabase, "", "database %q already exists in the shared Postgres but is not linked to this app", ident)
	}
	pw := randHex(24)
	progress("Creating database %s", ident)
	if _, err := psql(fmt.Sprintf(`CREATE ROLE "%[1]s" LOGIN PASSWORD '%[2]s';
CREATE DATABASE "%[1]s" OWNER "%[1]s";
REVOKE ALL ON DATABASE "%[1]s" FROM PUBLIC;`, ident, pw)); err != nil {
		return nil, err
	}
	d := &proto.Database{Engine: "postgres", Name: ident, User: ident, Host: pgContainer, CreatedAt: time.Now().UTC()}
	url := fmt.Sprintf("postgres://%s:%s@%s:5432/%s?sslmode=disable", ident, pw, pgContainer, ident)
	if err := setEnv(app, map[string]string{"DATABASE_URL": url}); err != nil {
		return nil, err
	}
	if err := writeJSON(dbFile(app), d); err != nil {
		return nil, err
	}
	if err := installBackupTimer(); err != nil {
		progress("warning: nightly backups not scheduled: %v", err)
	}
	return map[string]any{"database": d, "created": true}, nil
}

func dropDB(app string, d *proto.Database) error {
	if _, err := psql(fmt.Sprintf(`DROP DATABASE IF EXISTS "%[1]s" WITH (FORCE);
DROP ROLE IF EXISTS "%[1]s";`, d.Name)); err != nil {
		return err
	}
	os.Remove(dbFile(app))
	return nil
}

func dbShell(app, sql string) error {
	d, err := requireDB(app)
	if err != nil {
		return err
	}
	if err := ensurePostgres(); err != nil {
		return err
	}
	args := []string{"exec", "-i"}
	if sql == "" && isTerminal(os.Stdin) {
		args = append(args, "-t")
	}
	args = append(args, pgContainer, "psql", "-U", d.User, "-d", d.Name)
	if sql != "" {
		args = append(args, "-v", "ON_ERROR_STOP=1", "-c", sql)
	}
	cmd := exec.Command("docker", args...)
	cmd.Stdin, cmd.Stdout, cmd.Stderr = os.Stdin, os.Stdout, os.Stderr
	return cmd.Run()
}

func isTerminal(f *os.File) bool {
	fi, err := f.Stat()
	return err == nil && fi.Mode()&os.ModeCharDevice != 0
}

func backupDB(app string, d *proto.Database) (*proto.Backup, error) {
	if err := ensurePostgres(); err != nil {
		return nil, err
	}
	dir := filepath.Join(backupsDir, app)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, err
	}
	now := time.Now().UTC()
	path := filepath.Join(dir, now.Format("20060102-150405")+".dump")
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o600)
	if err != nil {
		return nil, err
	}
	progress("Dumping database %s", d.Name)
	var stderr bytes.Buffer
	cmd := exec.Command("docker", "exec", pgContainer, "pg_dump", "-U", "postgres", "-Fc", d.Name)
	cmd.Stdout, cmd.Stderr = f, &stderr
	err = cmd.Run()
	f.Close()
	if err != nil {
		os.Remove(path)
		return nil, &proto.Error{Code: proto.CodeDatabase, Message: "pg_dump failed", Logs: tail(stderr.String(), 10)}
	}
	pruneBackups(dir)
	fi, _ := os.Stat(path)
	return &proto.Backup{App: app, Path: path, Size: fi.Size(), CreatedAt: now}, nil
}

func listBackups(app string) []proto.Backup {
	matches, _ := filepath.Glob(filepath.Join(backupsDir, app, "*.dump"))
	sort.Strings(matches)
	var out []proto.Backup
	for _, m := range matches {
		if fi, err := os.Stat(m); err == nil {
			out = append(out, proto.Backup{App: app, Path: m, Size: fi.Size(), CreatedAt: fi.ModTime().UTC()})
		}
	}
	return out
}

func pruneBackups(dir string) {
	matches, _ := filepath.Glob(filepath.Join(dir, "*.dump"))
	sort.Strings(matches) // names are timestamps
	for len(matches) > keepBackups {
		os.Remove(matches[0])
		matches = matches[1:]
	}
}

func backupAll() (any, error) {
	entries, _ := os.ReadDir(appsDir)
	var done []*proto.Backup
	var failed []string
	for _, e := range entries {
		d, err := loadDB(e.Name())
		if err != nil || d == nil {
			continue
		}
		b, err := backupDB(e.Name(), d)
		if err != nil {
			failed = append(failed, e.Name()+": "+err.Error())
			continue
		}
		done = append(done, b)
	}
	if len(failed) > 0 {
		return nil, proto.Errf(proto.CodeDatabase, "", "backups failed: %s", strings.Join(failed, "; "))
	}
	return map[string]any{"backups": done}, nil
}

// dbRestore replaces the app's database with a dump read from stdin: custom
// format (pg_dump -Fc) or plain SQL. A safety backup is taken first.
func dbRestore(app string) (any, error) {
	d, err := requireDB(app)
	if err != nil {
		return nil, err
	}
	unlock, err := lockApp(app)
	if err != nil {
		return nil, err
	}
	defer unlock()
	safety, err := backupDB(app, d)
	if err != nil {
		return nil, err
	}
	progress("Safety backup: %s", safety.Path)
	in := bufio.NewReader(os.Stdin)
	head, _ := in.Peek(5)
	var cmd *exec.Cmd
	if string(head) == "PGDMP" {
		progress("Restoring custom-format dump")
		cmd = exec.Command("docker", "exec", "-i", pgContainer, "pg_restore", "-U", "postgres", "-d", d.Name,
			"--clean", "--if-exists", "--no-owner", "--role="+d.User, "--exit-on-error")
	} else {
		progress("Restoring plain SQL")
		cmd = exec.Command("docker", "exec", "-i", pgContainer, "psql", "-U", d.User, "-d", d.Name, "-v", "ON_ERROR_STOP=1", "-q")
	}
	var out bytes.Buffer
	cmd.Stdin, cmd.Stdout, cmd.Stderr = in, io.Discard, &out
	if err := cmd.Run(); err != nil {
		return nil, &proto.Error{Code: proto.CodeDatabase, Message: "restore failed; the safety backup is at " + safety.Path, Logs: tail(out.String(), 20)}
	}
	return map[string]any{"restored": true, "safety_backup": safety}, nil
}

func installBackupTimer() error {
	service := "[Unit]\nDescription=ship nightly database backups\n\n[Service]\nType=oneshot\nExecStart=/usr/local/bin/shipd db backup-all\n"
	timer := "[Unit]\nDescription=ship nightly database backups\n\n[Timer]\nOnCalendar=*-*-* 03:30:00\nRandomizedDelaySec=15m\nPersistent=true\n\n[Install]\nWantedBy=timers.target\n"
	svcPath := filepath.Join(systemdDir, backupUnit+".service")
	if b, err := os.ReadFile(svcPath); err == nil && string(b) == service {
		return nil
	}
	if err := os.WriteFile(svcPath, []byte(service), 0o644); err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(systemdDir, backupUnit+".timer"), []byte(timer), 0o644); err != nil {
		return err
	}
	combined("systemctl", "daemon-reload")
	if out, err := combined("systemctl", "enable", "--now", backupUnit+".timer"); err != nil {
		return errors.New(tail(out, 3))
	}
	return nil
}
