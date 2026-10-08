package cli

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/spf13/cobra"

	"github.com/semenov/homebase/internal/proto"
)

func dbCmd() *cobra.Command {
	db := &cobra.Command{
		Use:   "db",
		Short: "Postgres database for the deployed app (one per app, on your server)",
		Long: `Each app can get its own database in the server's shared Postgres.
The app receives DATABASE_URL; the database is not reachable from the internet.
Backups run nightly (the last 7 are kept in /var/lib/ship/backups/<app>/).`,
	}

	add := &cobra.Command{
		Use:   "add [postgres]",
		Short: "Create the app's database, set DATABASE_URL and restart the app",
		Args:  cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if len(args) == 1 && args[0] != "postgres" {
				return proto.Errf(proto.CodeUsage, "only postgres is supported for now", "unknown database engine %q", args[0])
			}
			r, app, err := prodTarget()
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
			emit(res, func(u *UI) {
				if !res.Created {
					u.OK("%s already has a database", app)
				} else {
					u.OK("Created a Postgres database for %s", app)
				}
				u.KV("Database", res.Database.Name)
				u.KV("Env", "DATABASE_URL"+u.p.dim(" (homebase env --prod --reveal)"))
				if res.Created && !res.Restarted {
					u.Para("The app gets it on the next deploy.")
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
			r, app, err := prodTarget()
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
			emit(res, func(u *UI) {
				u.OK("%s has a Postgres database", app)
				u.KV("Database", res.Database.Name+u.p.dim(" on "+res.Database.Host+", created "+ago(res.Database.CreatedAt)+" ago"))
				u.Head("BACKUPS")
				if len(res.Backups) == 0 {
					u.Line("    " + u.p.dim("none yet: homebase db backup"))
				}
				for _, b := range res.Backups {
					u.Line("    " + b.Path + "  " + u.p.dim(humanBytes(b.Size)))
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
			r, app, err := prodTarget()
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
			r, app, err := prodTarget()
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
				step("Downloading it to %s", local)
				if err := r.Download(b.Path, local); err != nil {
					return proto.Errf(proto.CodeSSH, "", "download failed: %v", err)
				}
			}
			emit(map[string]any{"backup": b, "local_path": local}, func(u *UI) {
				u.OK("Backed up %s", app)
				u.KV("Backup", b.Path+u.p.dim(" ("+humanBytes(b.Size)+")"))
				if local != "" {
					u.KV("Download", local)
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
			r, app, err := prodTarget()
			if err != nil {
				return err
			}
			if !yes {
				return proto.Errf(proto.CodeConfirm, fmt.Sprintf("run it again with --yes: homebase db restore %s --yes", args[0]), "restore overwrites the database of %q", app)
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
			emit(res, func(u *UI) {
				u.OK("Restored the database of %s from %s", app, args[0])
				if sb, ok := res["safety_backup"].(map[string]any); ok {
					u.KV("Before", fmt.Sprint(sb["path"])+u.p.dim(" (backup taken first)"))
				}
			})
			return nil
		},
	}
	restore.Flags().BoolVarP(&yes, "yes", "y", false, "confirm")

	db.AddCommand(add, info, shell, backup, restore)
	return db
}
