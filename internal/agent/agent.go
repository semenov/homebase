// Package agent implements shipd, the small helper ship installs on the server.
// Every command prints progress to stderr and exactly one proto.Response to stdout.
package agent

import (
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"syscall"

	"github.com/vsemenov/ship/internal/proto"
)

const (
	stateDir    = "/var/lib/ship"
	appsDir     = stateDir + "/apps"
	acmeWebroot = "/var/www/ship-acme" // must be readable by the nginx workers
	serverFile  = stateDir + "/server.json"
)

var appNameRe = regexp.MustCompile(`^[a-z0-9]([a-z0-9-]{0,40}[a-z0-9])?$`)

func progress(format string, args ...any) {
	fmt.Fprintf(os.Stderr, format+"\n", args...)
}

func Main(args []string) int {
	if len(args) == 0 {
		return reply(nil, proto.Errf(proto.CodeUsage, "", "usage: shipd <command> [flags]"))
	}
	cmd, rest := args[0], args[1:]
	var data any
	var err error
	switch cmd {
	case "version":
		fmt.Println("shipd")
		return 0
	case "setup":
		data, err = cmdSetup(rest)
	case "deploy":
		data, err = cmdDeploy(rest)
	case "build":
		data, err = cmdBuild(rest)
	case "has-image":
		data, err = cmdHasImage(rest)
	case "status":
		data, err = cmdStatus(rest)
	case "list":
		data, err = cmdList(rest)
	case "logs":
		// logs streams raw container output, no JSON envelope on success
		if err = cmdLogs(rest); err == nil {
			return 0
		}
	case "rollback":
		data, err = cmdRollback(rest)
	case "restart":
		data, err = cmdRestart(rest)
	case "env":
		data, err = cmdEnv(rest)
	case "db":
		if len(rest) > 0 && rest[0] == "shell" {
			if _, err = cmdDB(rest); err == nil {
				return 0
			}
			break
		}
		data, err = cmdDB(rest)
	case "destroy":
		data, err = cmdDestroy(rest)
	default:
		err = proto.Errf(proto.CodeUsage, "", "unknown command %q", cmd)
	}
	return reply(data, err)
}

func reply(data any, err error) int {
	resp := proto.Response{OK: err == nil}
	if err != nil {
		var pe *proto.Error
		if !errors.As(err, &pe) {
			pe = &proto.Error{Code: proto.CodeInternal, Message: err.Error()}
		}
		resp.Error = pe
	} else if data != nil {
		b, _ := json.Marshal(data)
		resp.Data = b
	}
	json.NewEncoder(os.Stdout).Encode(resp)
	if err != nil {
		return 1
	}
	return 0
}

func newFlags(name string) *flag.FlagSet {
	fs := flag.NewFlagSet(name, flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	return fs
}

func parse(fs *flag.FlagSet, args []string) error {
	if err := fs.Parse(args); err != nil {
		return proto.Errf(proto.CodeUsage, "", "%s: %v", fs.Name(), err)
	}
	return nil
}

func checkAppName(name string) error {
	if !appNameRe.MatchString(name) {
		return proto.Errf(proto.CodeConfig, "use lowercase letters, digits and dashes (max 42 chars)", "invalid app name %q", name)
	}
	return nil
}

func appDir(name string) string { return filepath.Join(appsDir, name) }

func loadApp(name string) (*proto.App, error) {
	if err := checkAppName(name); err != nil {
		return nil, err
	}
	b, err := os.ReadFile(filepath.Join(appDir(name), "app.json"))
	if errors.Is(err, os.ErrNotExist) {
		return nil, proto.Errf(proto.CodeNotFound, "run `ship ls` to see deployed apps", "app %q is not deployed on this server", name)
	}
	if err != nil {
		return nil, err
	}
	var a proto.App
	if err := json.Unmarshal(b, &a); err != nil {
		return nil, err
	}
	return &a, nil
}

func saveApp(a *proto.App) error {
	dir := appDir(a.Name)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	return writeJSON(filepath.Join(dir, "app.json"), a)
}

func writeJSON(path string, v any) error {
	b, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return err
	}
	return writeFileAtomic(path, b, 0o600)
}

func writeFileAtomic(path string, b []byte, mode os.FileMode) error {
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, b, mode); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

// lockApp serializes mutating operations on one app. The returned func releases the lock.
func lockApp(name string) (func(), error) {
	dir := appDir(name)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, err
	}
	f, err := os.OpenFile(filepath.Join(dir, "lock"), os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, err
	}
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		f.Close()
		return nil, proto.Errf(proto.CodeLocked, "wait for the other deploy to finish and retry", "another operation on %q is in progress", name)
	}
	return func() { syscall.Flock(int(f.Fd()), syscall.LOCK_UN); f.Close() }, nil
}

func listApps() ([]*proto.App, error) {
	entries, err := os.ReadDir(appsDir)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var apps []*proto.App
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		if a, err := loadApp(e.Name()); err == nil {
			apps = append(apps, a)
		}
	}
	return apps, nil
}
