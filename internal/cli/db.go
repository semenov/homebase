package cli

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/vsemenov/ship/internal/proto"
)

func dbCmd() *cobra.Command {
	db := &cobra.Command{
		Use:   "db",
		Short: "Postgres database for the app (shared server instance, one database per app)",
		Long: `Each app can get its own database in the server's shared Postgres.
The app receives DATABASE_URL; the database is not reachable from the internet.
Backups run nightly (last 7 kept in /var/lib/ship/backups/<app>/).`,
	}

	add := &cobra.Command{
		Use:   "add [postgres]",
		Short: "Create the app's database, set DATABASE_URL and restart the app",
		Args:  cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if len(args) == 1 && args[0] != "postgres" {
				return proto.Errf(proto.CodeUsage, "only postgres is supported for now", "unknown database engine %q", args[0])
			}
			r, app, err := target()
			if err != nil {
				return err
			}
			var res struct {
				Database  proto.Database `json:"database"`
				Created   bool           `json:"created"`
				Restarted bool           `json:"restarted"`
			}
			if err := r.Agent(nil, &res, "db", "add", "--app", app); err != nil {
				return err
			}
			if res.Created {
				res.Restarted = restartIfDeployed(r, app)
			}
			emit(res, func() {
				if !res.Created {
					fmt.Printf("✓ %s already has database %s (DATABASE_URL is set)\n", app, res.Database.Name)
					return
				}
				fmt.Printf("✓ created postgres database %s for %s\n", res.Database.Name, app)
				fmt.Println("  DATABASE_URL is set in the app's env (see `ship env ls --reveal`)")
				if !res.Restarted {
					fmt.Println("  it will be available on the next deploy")
				}
			})
			return nil
		},
	}

	info := &cobra.Command{
		Use:   "info",
		Short: "Show the database and its backups",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			r, app, err := target()
			if err != nil {
				return err
			}
			var res struct {
				Database proto.Database `json:"database"`
				Backups  []proto.Backup `json:"backups"`
			}
			if err := r.Agent(nil, &res, "db", "info", "--app", app); err != nil {
				return err
			}
			emit(res, func() {
				fmt.Printf("%s: postgres database %s on %s (created %s ago)\n", app, res.Database.Name, res.Database.Host, ago(res.Database.CreatedAt))
				if len(res.Backups) == 0 {
					fmt.Println("  no backups yet (`ship db backup`)")
				}
				for _, b := range res.Backups {
					fmt.Printf("  backup %s  %s\n", b.Path, humanBytes(b.Size))
				}
			})
			return nil
		},
	}

	var sql string
	shell := &cobra.Command{
		Use:   "shell",
		Short: "Open psql, or run one statement with -c",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			r, app, err := target()
			if err != nil {
				return err
			}
			if sql != "" {
				if err := r.Stream("db", "shell", "--app", app, "-c", sql); err != nil {
					return proto.Errf(proto.CodeDatabase, "", "query failed")
				}
				return nil
			}
			return r.Interactive("db", "shell", "--app", app)
		},
	}
	shell.Flags().StringVarP(&sql, "command", "c", "", "run a single SQL statement and exit")

	var download bool
	var out string
	backup := &cobra.Command{
		Use:   "backup",
		Short: "Dump the database now (optionally download it)",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			r, app, err := target()
			if err != nil {
				return err
			}
			var b proto.Backup
			if err := r.Agent(nil, &b, "db", "backup", "--app", app); err != nil {
				return err
			}
			local := ""
			if download || out != "" {
				local = out
				if local == "" {
					local = app + "-" + strings.TrimSuffix(filepath.Base(b.Path), ".dump") + ".dump"
				}
				step("Downloading to %s", local)
				if err := r.Download(b.Path, local); err != nil {
					return proto.Errf(proto.CodeSSH, "", "download failed: %v", err)
				}
			}
			emit(map[string]any{"backup": b, "local_path": local}, func() {
				fmt.Printf("✓ backup %s (%s)\n", b.Path, humanBytes(b.Size))
				if local != "" {
					fmt.Printf("  downloaded to %s\n", local)
				}
			})
			return nil
		},
	}
	backup.Flags().BoolVar(&download, "download", false, "also save the dump to the current directory")
	backup.Flags().StringVarP(&out, "output", "o", "", "download the dump to this path")

	var yes bool
	restore := &cobra.Command{
		Use:   "restore FILE",
		Short: "Replace the database with a dump (pg_dump -Fc or plain SQL); a safety backup is taken first",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			r, app, err := target()
			if err != nil {
				return err
			}
			if !yes {
				return proto.Errf(proto.CodeConfirm, fmt.Sprintf("re-run with --yes: `ship db restore %s --yes`", args[0]), "restore overwrites the database of %q", app)
			}
			f, err := os.Open(args[0])
			if err != nil {
				return proto.Errf(proto.CodeUsage, "", "%v", err)
			}
			defer f.Close()
			var res map[string]any
			if err := r.Agent(f, &res, "db", "restore", "--app", app); err != nil {
				return err
			}
			emit(res, func() { fmt.Printf("✓ restored %s from %s\n", app, args[0]) })
			return nil
		},
	}
	restore.Flags().BoolVarP(&yes, "yes", "y", false, "confirm")

	db.AddCommand(add, info, shell, backup, restore)
	return db
}

// restartIfDeployed restarts the app so it picks up env changes. Returns true if it did.
func restartIfDeployed(r *Remote, app string) bool {
	var st proto.AppStatus
	if err := r.Agent(nil, &st, "status", "--app", app); err != nil || st.Current == nil {
		return false
	}
	step("Restarting %s to apply changes", app)
	return r.Agent(nil, nil, "restart", "--app", app, "--timeout", (90*time.Second).String()) == nil
}
