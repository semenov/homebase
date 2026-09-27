// Package cli implements the ship command line client.
package cli

import (
	_ "embed"
	"errors"
	"fmt"
	"os"
	"sort"
	"strconv"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/spf13/cobra"

	"github.com/vsemenov/ship/internal/detect"
	"github.com/vsemenov/ship/internal/proto"
)

//go:embed agents.md
var agentDocs string

var (
	flagApp    string
	flagServer string
	flagYes    bool
)

func Main() int {
	root := newRoot()
	if err := root.Execute(); err != nil {
		return fail(err)
	}
	return 0
}

func newRoot() *cobra.Command {
	var d deployOpts
	root := &cobra.Command{
		Use:   "ship [dir]",
		Short: "Deploy web apps to your own server over SSH",
		Long: `ship deploys a web app from the current directory to your own server.
AI agents: run ` + "`ship docs`" + ` first; it explains everything and summarizes this project.

  ship init root@1.2.3.4     one-time: remember the server (--install sets up docker + caddy)
  ship                       build, upload and release the app in the current directory

The app gets https://<name>.<ip>.sslip.io by default, or your --domain.
Every command accepts --json for machine-readable output. Run ` + "`ship docs`" + ` for the full guide.`,
		Args:          cobra.MaximumNArgs(1),
		SilenceUsage:  true,
		SilenceErrors: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			d.dir = "."
			if len(args) == 1 {
				d.dir = args[0]
			}
			return runDeploy(d)
		},
	}
	root.SetFlagErrorFunc(func(_ *cobra.Command, err error) error {
		return &proto.Error{Code: proto.CodeUsage, Message: err.Error(), Hint: "see `ship --help`"}
	})
	pf := root.PersistentFlags()
	pf.BoolVar(&jsonOut, "json", os.Getenv("SHIP_JSON") == "1", "print the result as JSON on stdout (env SHIP_JSON=1)")
	pf.StringVarP(&flagApp, "app", "a", "", "app name (default: ship.toml name, or the directory name)")
	pf.StringVarP(&flagServer, "server", "s", "", "ssh target user@host (default: ship.toml, $SHIP_SERVER, or `ship init`)")

	addDeployFlags(root, &d)

	deploy := &cobra.Command{
		Use:   "deploy [dir]",
		Short: "Build and release the app (same as plain `ship`)",
		Args:  cobra.MaximumNArgs(1),
		RunE:  root.RunE,
	}
	addDeployFlags(deploy, &d)

	root.AddCommand(deploy, initCmd(), statusCmd(), listCmd(), logsCmd(), rollbackCmd(), restartCmd(), envCmd(), dbCmd(), destroyCmd(), docsCmd(), agentsCmd())
	return root
}

func addDeployFlags(c *cobra.Command, d *deployOpts) {
	f := c.Flags()
	f.StringVar(&d.domain, "domain", "", "domain for the app (its DNS A record must point at the server)")
	f.IntVarP(&d.port, "port", "p", 0, "port the app listens on inside the container (default: detected)")
	f.StringVar(&d.health, "health", "", "HTTP path that must answer non-5xx before traffic switches (default /)")
	f.StringVar(&d.start, "start", "", "start command when no Dockerfile is present")
	f.StringArrayVar(&d.volumes, "volume", nil, "container path to keep across deploys, e.g. /data (repeatable)")
	f.StringVar(&d.release, "release", "", "command run in the new image before traffic switches, e.g. \"npm run migrate\"")
	f.BoolVar(&d.remoteBuild, "remote-build", false, "build the image on the server instead of locally")
	f.BoolVar(&d.noSave, "no-save", false, "do not write ship.toml after the first deploy")
	f.DurationVar(&d.timeout, "timeout", 90*time.Second, "how long to wait for the app to become healthy")
}

func resolveServer(p *Project) (string, error) {
	s := firstNonEmpty(flagServer, p.Server, os.Getenv("SHIP_SERVER"), loadGlobal().DefaultServer)
	if s == "" {
		return "", proto.Errf(proto.CodeConfig, "run `ship init user@host` once, or pass --server user@host", "no server configured")
	}
	return s, nil
}

// target resolves server and app name for commands that act on an existing app.
func target() (*Remote, string, error) {
	dir, _ := os.Getwd()
	proj, _, err := loadProject(dir)
	if err != nil {
		return nil, "", err
	}
	server, err := resolveServer(proj)
	if err != nil {
		return nil, "", err
	}
	r, err := Connect(server)
	if err != nil {
		return nil, "", err
	}
	return r, firstNonEmpty(flagApp, proj.Name, appNameFrom(dir)), nil
}

func initCmd() *cobra.Command {
	var base string
	var install, noDefault bool
	c := &cobra.Command{
		Use:   "init user@host",
		Short: "Prepare a server and make it the default",
		Long: `Connects over SSH, installs the shipd helper, detects the nginx setup and
remembers the server as default for future deploys. Safe to re-run.

With --install, missing docker and caddy are installed (Debian/Ubuntu).
Caddy is the default reverse proxy (automatic HTTPS). If the server already
runs nginx on :443, ship uses it instead (with certbot for certificates).
With --base-domain apps.example.com (and a wildcard DNS record *.apps.example.com
pointing at the server), apps get <name>.apps.example.com instead of sslip.io.`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			server := args[0]
			step("Connecting to %s", server)
			r, err := Connect(server)
			if err != nil {
				return err
			}
			a := []string{"setup"}
			if ip := PublicIP(server); ip != "" {
				a = append(a, "--ip", ip)
			}
			if base != "" {
				a = append(a, "--base-domain", base)
			}
			if install {
				a = append(a, "--install")
			}
			var si proto.ServerInfo
			if err := r.Agent(nil, &si, a...); err != nil {
				return err
			}
			g := loadGlobal()
			if !noDefault && g.DefaultServer != server {
				g.DefaultServer = server
				if err := saveGlobal(g); err != nil {
					return err
				}
			}
			emit(si, func() {
				fmt.Printf("✓ %s is ready (%s, docker %s, %s %s)\n", server, si.Arch, si.Docker, si.Proxy, si.ProxyVersion)
				if si.Proxy == "nginx" {
					fmt.Printf("  nginx mode: https on %s, configs in %s\n", strings.Join(si.SSLListen, ", "), si.NginxDir)
				}
				if si.BaseDomain != "" {
					fmt.Printf("  apps get <name>.%s\n", si.BaseDomain)
				} else if si.PublicIP != "" {
					fmt.Printf("  apps get <name>.%s.sslip.io (use --base-domain for your own wildcard domain)\n", strings.ReplaceAll(si.PublicIP, ".", "-"))
				}
				if !noDefault {
					fmt.Printf("  default server saved to %s\n", globalPath())
				}
				fmt.Println("  next: cd into your app and run `ship`")
				if hint := agentsHint(); hint != "" {
					fmt.Println(hint)
				}
			})
			return nil
		},
	}
	c.Flags().StringVar(&base, "base-domain", "", "wildcard base domain for apps, e.g. apps.example.com")
	c.Flags().BoolVar(&install, "install", false, "install missing docker and caddy")
	c.Flags().BoolVar(&noDefault, "no-default", false, "do not make this the default server")
	return c
}

func statusCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "status",
		Short: "Show the app's URL, release and health",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			r, app, err := target()
			if err != nil {
				return err
			}
			var st proto.AppStatus
			if err := r.Agent(nil, &st, "status", "--app", app); err != nil {
				return err
			}
			emit(st, func() {
				health := "healthy"
				if !st.Healthy {
					health = "UNHEALTHY"
				}
				fmt.Printf("%s  %s  %s (%s)\n", st.Name, st.URL, st.State, health)
				if st.Current != nil {
					fmt.Printf("  release  %s · %s · deployed %s ago\n", st.Current.ID, st.Current.Image, ago(st.Current.DeployedAt))
				}
				if st.Previous != nil {
					fmt.Printf("  previous %s (ship rollback)\n", st.Previous.ID)
				}
				for _, v := range st.Volumes {
					fmt.Printf("  volume   %s (persistent)\n", v)
				}
				if st.Database != nil {
					fmt.Printf("  database %s %s (DATABASE_URL, `ship db shell`)\n", st.Database.Engine, st.Database.Name)
				}
				if st.Restarts > 0 {
					fmt.Printf("  ! container restarted %d times, see `ship logs`\n", st.Restarts)
				}
			})
			return nil
		},
	}
}

func listCmd() *cobra.Command {
	return &cobra.Command{
		Use:     "ls",
		Aliases: []string{"list", "apps"},
		Short:   "List apps on the server",
		Args:    cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			r, _, err := target()
			if err != nil {
				return err
			}
			var out struct {
				Server proto.ServerInfo   `json:"server"`
				Apps   []*proto.AppStatus `json:"apps"`
			}
			if err := r.Agent(nil, &out, "list"); err != nil {
				return err
			}
			emit(out, func() {
				if len(out.Apps) == 0 {
					fmt.Println("no apps deployed yet")
					return
				}
				sort.Slice(out.Apps, func(i, j int) bool { return out.Apps[i].Name < out.Apps[j].Name })
				tw := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
				fmt.Fprintln(tw, "APP\tURL\tSTATE\tRELEASE\tDEPLOYED")
				for _, a := range out.Apps {
					state := a.State
					if a.State == "running" && !a.Healthy {
						state = "unhealthy"
					}
					rel, when := "-", "-"
					if a.Current != nil {
						rel, when = a.Current.ID, ago(a.Current.DeployedAt)+" ago"
					}
					fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\n", a.Name, a.URL, state, rel, when)
				}
				tw.Flush()
			})
			return nil
		},
	}
}

func logsCmd() *cobra.Command {
	var n int
	var follow bool
	var since string
	c := &cobra.Command{
		Use:   "logs",
		Short: "Print the app's container logs",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			r, app, err := target()
			if err != nil {
				return err
			}
			a := []string{"logs", "--app", app, "-n", strconv.Itoa(n)}
			if follow {
				a = append(a, "-f")
			}
			if since != "" {
				a = append(a, "--since", since)
			}
			if err := r.Stream(a...); err != nil {
				return proto.Errf(proto.CodeNotFound, "check `ship status`", "could not read logs of %q", app)
			}
			return nil
		},
	}
	c.Flags().IntVarP(&n, "lines", "n", 100, "number of lines from the end")
	c.Flags().BoolVarP(&follow, "follow", "f", false, "keep streaming new lines")
	c.Flags().StringVar(&since, "since", "", "only lines newer than this, e.g. 10m or 2026-09-27T10:00:00")
	return c
}

func releaseCmd(use, short, agentCmd, verb string) *cobra.Command {
	var timeout time.Duration
	c := &cobra.Command{
		Use:   use,
		Short: short,
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			r, app, err := target()
			if err != nil {
				return err
			}
			var res proto.DeployResult
			if err := r.Agent(nil, &res, agentCmd, "--app", app, "--timeout", timeout.String()); err != nil {
				return err
			}
			emit(res, func() {
				fmt.Printf("✓ %s %s: %s now runs %s\n", res.App, verb, res.URL, res.Release.Image)
			})
			return nil
		},
	}
	c.Flags().DurationVar(&timeout, "timeout", 90*time.Second, "how long to wait for the app to become healthy")
	return c
}

func rollbackCmd() *cobra.Command {
	return releaseCmd("rollback", "Switch back to the previous release (zero downtime)", "rollback", "rolled back")
}

func restartCmd() *cobra.Command {
	return releaseCmd("restart", "Restart the current release (zero downtime, picks up env changes)", "restart", "restarted")
}

func envCmd() *cobra.Command {
	var reveal, noRestart bool
	env := &cobra.Command{
		Use:   "env",
		Short: "Manage environment variables (stored on the server)",
	}
	show := func(res map[string]any) {
		emit(res, func() {
			vars, _ := res["env"].(map[string]any)
			keys := make([]string, 0, len(vars))
			for k := range vars {
				keys = append(keys, k)
			}
			sort.Strings(keys)
			if len(keys) == 0 {
				fmt.Println("no variables set")
			}
			for _, k := range keys {
				fmt.Printf("%s=%v\n", k, vars[k])
			}
		})
	}
	run := func(op string, args []string) error {
		r, app, err := target()
		if err != nil {
			return err
		}
		a := []string{"env", op, "--app", app}
		if reveal {
			a = append(a, "--reveal")
		}
		var res map[string]any
		if err := r.Agent(nil, &res, append(a, args...)...); err != nil {
			return err
		}
		if op != "list" && !noRestart {
			res["restarted"] = restartIfDeployed(r, app)
		}
		show(res)
		return nil
	}
	ls := &cobra.Command{Use: "ls", Aliases: []string{"list"}, Short: "List variables (values hidden unless --reveal)", Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error { return run("list", nil) }}
	ls.Flags().BoolVar(&reveal, "reveal", false, "show values")
	set := &cobra.Command{Use: "set KEY=VALUE...", Short: "Set variables and restart the app", Args: cobra.MinimumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error { return run("set", args) }}
	unset := &cobra.Command{Use: "unset KEY...", Short: "Remove variables and restart the app", Args: cobra.MinimumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error { return run("unset", args) }}
	for _, c := range []*cobra.Command{set, unset} {
		c.Flags().BoolVar(&noRestart, "no-restart", false, "do not restart the app")
	}
	env.AddCommand(ls, set, unset)
	return env
}

func destroyCmd() *cobra.Command {
	var data bool
	c := &cobra.Command{
		Use:   "destroy",
		Short: "Remove the app (containers, images, route); data is kept unless --data",
		Long: `Removes the app's containers, images and proxy route.

Volumes, the database and env vars are kept, so deploying the app again brings
its data back. With --data they are deleted too (the database gets a final
backup in /var/lib/ship/backups/<app>/ first).`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			r, app, err := target()
			if err != nil {
				return err
			}
			if !flagYes {
				what := "removes %q from %s (data is kept)"
				if data {
					what = "removes %q from %s including its volumes and database"
				}
				return proto.Errf(proto.CodeConfirm, fmt.Sprintf("re-run with --yes: `ship destroy -a %s%s --yes`", app, map[bool]string{true: " --data"}[data]), what, app, r.Target)
			}
			a := []string{"destroy", "--app", app}
			if data {
				a = append(a, "--data")
			}
			var res map[string]any
			if err := r.Agent(nil, &res, a...); err != nil {
				return err
			}
			emit(res, func() {
				fmt.Printf("✓ %s destroyed\n", app)
				if v, ok := res["kept_volumes"]; ok {
					fmt.Printf("  kept volumes: %v\n", v)
				}
				if v, ok := res["kept_database"]; ok {
					fmt.Printf("  kept database: %v\n", v)
				}
				if _, ok := res["kept_volumes"]; ok || res["kept_database"] != nil {
					fmt.Printf("  a new deploy reuses them; delete with `ship destroy -a %s --data --yes`\n", app)
				}
				if v, ok := res["final_backup"]; ok {
					fmt.Printf("  final database backup: %v\n", v)
				}
			})
			return nil
		},
	}
	c.Flags().BoolVarP(&flagYes, "yes", "y", false, "confirm")
	c.Flags().BoolVar(&data, "data", false, "also delete volumes, the database and env vars")
	return c
}

func docsCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "docs",
		Short: "Print the full guide plus a summary of the current project (start here, AI agents)",
		Args:  cobra.NoArgs,
		Run: func(cmd *cobra.Command, args []string) {
			fmt.Print(agentDocs)
			fmt.Print(projectSummary())
		},
	}
}

// projectSummary describes what ship would do in the current directory, so an
// agent gets a concrete plan without running anything. It never touches the network.
func projectSummary() string {
	dir, _ := os.Getwd()
	var b strings.Builder
	w := func(format string, args ...any) { fmt.Fprintf(&b, format+"\n", args...) }
	w("\n## This project (%s)\n", dir)
	proj, found, err := loadProject(dir)
	if err != nil {
		w("- ship.toml is invalid: %v", err)
		return b.String()
	}
	if found {
		w("- ship.toml: present (already deployed with ship before, or configured by hand)")
	} else {
		w("- ship.toml: none yet; the first `ship` writes it")
	}
	w("- app name: %s", firstNonEmpty(flagApp, proj.Name, appNameFrom(dir)))
	if server, err := resolveServer(proj); err == nil {
		w("- server: %s", server)
	} else {
		w("- server: NOT CONFIGURED; ask the user for one, then `ship init user@host`")
	}
	if plan, err := detect.Detect(dir, proj.Dockerfile, proj.Start); err != nil {
		var pe *proto.Error
		if errors.As(err, &pe) {
			w("- build: cannot detect (%s); %s", pe.Message, pe.Hint)
		}
	} else {
		how := "generated Dockerfile"
		if plan.Dockerfile != "" {
			how = plan.Dockerfile
		}
		port := plan.Port
		if proj.Port > 0 {
			port = proj.Port
		}
		w("- build: %s via %s; the app must listen on 0.0.0.0:$PORT (PORT=%d)", plan.Stack, how, port)
	}
	if proj.Domain != "" {
		w("- domain: %s", proj.Domain)
	}
	if len(proj.Volumes) > 0 {
		w("- persistent paths: %s (DATA_DIR=%s)", strings.Join(proj.Volumes, ", "), proj.Volumes[0])
	} else {
		w("- persistent paths: none; add `volumes = [\"/data\"]` to ship.toml if the app writes files or uses SQLite")
	}
	if proj.Release != "" {
		w("- release command: %s", proj.Release)
	}
	w("\nNext: `ship --json` to deploy, then `ship status --json`. If the app needs Postgres, run `ship db add` first.")
	return b.String()
}

func ago(t time.Time) string {
	d := time.Since(t)
	switch {
	case d < time.Minute:
		return strconv.Itoa(int(d.Seconds())) + "s"
	case d < time.Hour:
		return strconv.Itoa(int(d.Minutes())) + "m"
	case d < 48*time.Hour:
		return strconv.Itoa(int(d.Hours())) + "h"
	}
	return strconv.Itoa(int(d.Hours()/24)) + "d"
}
