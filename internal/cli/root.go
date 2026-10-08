// Package cli implements the homebase command line.
package cli

import (
	_ "embed"
	"errors"
	"fmt"
	"os"
	"runtime"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/semenov/homebase/internal/config"
	"github.com/semenov/homebase/internal/detect"
)

//go:embed docs.md
var guide string

// Version is set at build time (make sets it from git tags).
var Version = "dev"

func Main() int {
	root := newRoot()
	if err := root.Execute(); err != nil {
		return fail(err)
	}
	return 0
}

func newRoot() *cobra.Command {
	var o upOpts
	root := &cobra.Command{
		Use:           "homebase [dir]",
		Short:         "Run your projects: dev servers on your Mac, deploys to your server",
		Args:          cobra.MaximumNArgs(1),
		Version:       Version,
		SilenceUsage:  true,
		SilenceErrors: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			if len(args) == 0 && cmd.Flags().NFlag() == 0 {
				if runtime.GOOS != "darwin" {
					fmt.Print(helpText())
					return nil
				}
				cfg, err := loadConfig()
				if err != nil {
					return err
				}
				if !isProject(cfg) {
					list(cfg, true, false)
					return nil
				}
			}
			return runUp(firstArg(args), o)
		},
	}
	root.SetFlagErrorFunc(func(_ *cobra.Command, err error) error {
		return errf(CodeUsage, "see `homebase --help`", "%v", err)
	})
	root.SetHelpFunc(func(c *cobra.Command, args []string) {
		if c == root {
			fmt.Print(helpText())
			return
		}
		fmt.Println(c.Long + "\n")
		if c.Long == "" {
			fmt.Println(c.Short + "\n")
		}
		fmt.Print(c.UsageString())
	})
	pf := root.PersistentFlags()
	pf.BoolVar(&jsonOut, "json", os.Getenv("HOMEBASE_JSON") == "1", "print the result as JSON on stdout (env HOMEBASE_JSON=1)")
	pf.StringVarP(&flagApp, "app", "a", "", "project name (default: the project of the current folder)")
	pf.StringVarP(&flagServer, "server", "s", "", "server user@host (default: [deploy] server, or `homebase server add`)")
	addUpFlags(root, &o)

	startC := &cobra.Command{
		Use:   "start [dir]",
		Short: "Start the project's dev server (same as plain `homebase`)",
		Args:  cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return runUp(firstArg(args), o)
		},
	}
	addUpFlags(startC, &o)

	version := &cobra.Command{
		Use:   "version",
		Short: "Print the version",
		Args:  cobra.NoArgs,
		Run: func(cmd *cobra.Command, args []string) {
			emit(map[string]string{"version": Version}, func(u *UI) { u.Line(Version) })
		},
	}

	root.AddCommand(startC, statusCmd(), listCmd(), logsCmd(), openCmd(), restartCmd(), stopCmd(), forgetCmd(),
		shareCmd(), unshareCmd(), envCmd(), initCmd(), uninstallCmd(),
		deployCmd(), rollbackCmd(), dbCmd(), destroyCmd(), ejectCmd(), serverCmd(),
		agentsCmd(), docsCmd(), version, proxyCmd(), tunnelCmd())
	root.CompletionOptions.HiddenDefaultCmd = true
	return root
}

func addUpFlags(c *cobra.Command, o *upOpts) {
	f := c.Flags()
	f.StringVar(&o.start, "start", "", "command that runs the dev server on $PORT (default: detected)")
	f.IntVarP(&o.port, "port", "p", 0, "port to use (default: the previous one, or the first free from 4000)")
	f.StringArrayVarP(&o.env, "env", "e", nil, "extra environment variable KEY=VALUE (repeatable)")
	f.BoolVar(&o.noSave, "no-save", false, "don't write homebase.toml")
	f.DurationVar(&o.timeout, "timeout", 60*time.Second, "how long to wait for the port to open")
}

func firstArg(args []string) string {
	if len(args) > 0 {
		return args[0]
	}
	return ""
}

type cmdDoc struct{ name, desc string }

func helpText() string {
	p := newPalette(os.Stdout)
	var b strings.Builder
	row := func(name, desc string, width int) {
		b.WriteString(fmt.Sprintf("    %s%s%s\n", p.blue(name), strings.Repeat(" ", max(1, width-len(name))), p.dim(desc)))
	}
	rows := func(width int, docs ...cmdDoc) {
		for _, d := range docs {
			row(d.name, d.desc, width)
		}
	}
	b.WriteString("\n  " + p.bold(p.fade("⌂ homebase")) + "  " + p.dim(Version) + "\n")
	b.WriteString("  " + p.bold("Your projects: dev servers on your Mac, deploys to your server.") + "\n")
	b.WriteString("  " + p.dim("Real URLs for both, one command each, never interactive.") + "\n\n")

	b.WriteString("  " + p.violet("QUICK START") + "\n")
	rows(36,
		cmdDoc{"homebase init", "once: http://<name>.localhost URLs"},
		cmdDoc{"cd my-app && homebase", "detect, start, give it a URL"},
		cmdDoc{"homebase server add root@1.2.3.4", "once: your server (--domain example.com)"},
		cmdDoc{"homebase deploy", "release it on your server, with HTTPS"},
	)

	b.WriteString("\n  " + p.violet("ON THIS MAC") + p.dim("  (the current folder's dev server; -a NAME for another)") + "\n")
	rows(18,
		cmdDoc{"homebase [dir]", "start the project here (also: homebase start)"},
		cmdDoc{"status", "dev server, share and deployed app of this project"},
		cmdDoc{"ls", "every dev server, and the apps on your server"},
		cmdDoc{"logs", "its output  (-f to follow, -n for line count)"},
		cmdDoc{"open", "open it in the browser"},
		cmdDoc{"restart", "restart it, re-reading homebase.toml"},
		cmdDoc{"stop", "stop it (stays stopped after a reboot)"},
		cmdDoc{"forget", "stop it and remove it from homebase; files stay"},
		cmdDoc{"share", "publish at https://<name>.dev.<domain>  (--private)"},
		cmdDoc{"unshare", "stop publishing it"},
		cmdDoc{"env", "its [env] in homebase.toml  (set K=V, unset K)"},
		cmdDoc{"init", "set up this Mac  (--lan)"},
		cmdDoc{"uninstall", "stop everything homebase runs here; settings stay"},
	)

	b.WriteString("\n  " + p.violet("ON YOUR SERVER") + p.dim("  (--prod: status, logs, open, restart, env of the deployed app)") + "\n")
	rows(18,
		cmdDoc{"deploy [dir]", "build and release the project (zero downtime)"},
		cmdDoc{"rollback", "back to the previous release"},
		cmdDoc{"db", "its Postgres: add, info, shell, backup, restore"},
		cmdDoc{"destroy", "remove the app; its data is kept unless --data"},
		cmdDoc{"eject", "write the generated Dockerfile into the project"},
		cmdDoc{"server", "your server; `server add user@host` sets one up"},
	)

	b.WriteString("\n  " + p.violet("OPTIONS") + p.dim("  (for homebase / start)") + "\n")
	rows(18,
		cmdDoc{"--start '<cmd>'", "how to run it, listening on $PORT (default: detected)"},
		cmdDoc{"-p, --port N", "use this port"},
		cmdDoc{"-e, --env K=V", "extra environment variable (repeatable)"},
		cmdDoc{"--no-save", "don't write homebase.toml"},
		cmdDoc{"--timeout 60s", "how long to wait for the port"},
	)
	b.WriteString("\n  " + p.violet("EVERYWHERE") + "\n")
	rows(18,
		cmdDoc{"-a, --app NAME", "act on this project instead of the current folder's"},
		cmdDoc{"-s, --server H", "use this server, user@host"},
		cmdDoc{"--json", "one JSON object on stdout, for scripts and agents"},
	)
	b.WriteString("\n  " + p.violet("AGENTS") + "\n")
	rows(18,
		cmdDoc{"agents", "tell your coding agents about homebase"},
		cmdDoc{"docs", "the full guide, plus a plan for this folder"},
	)

	b.WriteString("\n  " + p.dim("AI agents: read `homebase docs` first.") + "\n")
	b.WriteString("  " + p.dim("docs: https://github.com/semenov/homebase") + "\n\n")
	return b.String()
}

func docsCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "docs",
		Short: "The full guide, plus a plan for the current folder",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, err := loadConfig()
			if err != nil {
				return err
			}
			plan := folderPlan(cfg)
			if jsonOut {
				emit(map[string]any{"guide": guide, "folder": plan}, nil)
				return nil
			}
			fmt.Print(guide)
			fmt.Print("\n## This folder\n\n")
			line := func(k, format string, a ...any) { fmt.Printf("    %-16s%s\n", k, fmt.Sprintf(format, a...)) }
			line("folder", "%s", short(plan.Dir))
			switch {
			case plan.ProjectFile && plan.FromShip:
				line("homebase.toml", "no, but ship.toml: read as [deploy], moved into homebase.toml on the next save")
			case plan.ProjectFile:
				line("homebase.toml", "yes")
			default:
				line("homebase.toml", "no (written on the first run)")
			}
			fmt.Print("\n  Dev server (this Mac)\n\n")
			if plan.Server != "" {
				line("server", "%s (%s, %s)", plan.Server, plan.State, plan.URL)
			} else {
				line("server", "none yet")
			}
			if plan.Detected != nil {
				line("detected", "%s: %s", plan.Detected.Stack, plan.Detected.Command)
				if plan.Detected.Install != "" {
					line("dependencies", "missing: run %s first", plan.Detected.Install)
				}
			} else if plan.Server == "" && !plan.ProjectFile {
				line("detected", "nothing: pass --start '<command listening on $PORT>'")
			}
			line("next", "%s", plan.Next)
			d := plan.Deploy
			fmt.Print("\n  Deploy (your server)\n\n")
			line("app", "%s", d.App)
			if d.Server != "" {
				line("server", "%s", d.Server)
			} else {
				line("server", "NONE: ask the user for one, then `homebase server add user@host`")
			}
			if d.Problem != "" {
				line("build", "cannot detect: %s", d.Problem)
			} else {
				line("build", "%s via %s; the app must listen on 0.0.0.0:$PORT (PORT=%d)", d.Stack, d.Via, d.Port)
				if d.Context != nil {
					line("image", "%s", d.Context)
					for _, w := range d.Context.Warnings {
						line("WARNING", "%s", w)
					}
				}
			}
			if d.Domain != "" {
				line("domain", "%s", d.Domain)
			}
			if len(d.Volumes) > 0 {
				line("persistent", "%s (DATA_DIR=%s)", strings.Join(d.Volumes, ", "), d.Volumes[0])
			} else {
				line("persistent", "none; add volumes = [\"/data\"] under [deploy] if the app writes files or uses SQLite")
			}
			if d.Release != "" {
				line("release cmd", "%s", d.Release)
			}
			line("next", "%s", d.Next)
			return nil
		},
	}
}

type folder struct {
	Dir         string         `json:"dir"`
	ProjectFile bool           `json:"project_file"`
	FromShip    bool           `json:"from_ship_toml,omitempty"`
	Server      string         `json:"server,omitempty"`
	State       string         `json:"state,omitempty"`
	URL         string         `json:"url,omitempty"`
	Detected    *detect.Result `json:"detected,omitempty"`
	Next        string         `json:"next"`
	Deploy      deployPlan     `json:"deploy"`
}

// deployPlan is what `homebase deploy` would do here. It never touches the network.
type deployPlan struct {
	App      string       `json:"app"`
	Server   string       `json:"server,omitempty"`
	Deployed bool         `json:"configured"` // homebase.toml has a [deploy] table
	Stack    string       `json:"stack,omitempty"`
	Via      string       `json:"via,omitempty"` // generated Dockerfile, or the project's
	Port     int          `json:"port,omitempty"`
	Context  *ContextInfo `json:"image,omitempty"`
	Problem  string       `json:"problem,omitempty"`
	Domain   string       `json:"domain,omitempty"`
	Volumes  []string     `json:"volumes,omitempty"`
	Release  string       `json:"release,omitempty"`
	Next     string       `json:"next"`
}

func folderPlan(cfg *config.Config) folder {
	cwd, _ := os.Getwd()
	dir, _ := projectDir(cfg, "")
	f := folder{Dir: dir, ProjectFile: config.FindProjectDir(cwd) != ""}
	if p, _ := config.LoadProject(dir); p != nil {
		f.FromShip = p.FromShip
	}
	if names := serversFor(cfg, cwd); len(names) == 1 {
		in := info(cfg, names[0])
		f.Server, f.State, f.URL = in.Name, in.State, in.URL
	}
	f.Detected = detect.Detect(dir)
	switch {
	case f.Detected != nil && f.Detected.Install != "":
		f.Next = f.Detected.Install + ", then `homebase --json`"
	case f.State == "running":
		f.Next = "it is running; `homebase status --json` for details"
	case f.Server != "" || f.ProjectFile || f.Detected != nil:
		f.Next = "`homebase --json`"
	default:
		f.Next = "`homebase --start '<command listening on $PORT>' --json`"
	}
	f.Deploy = planDeploy(cfg, dir)
	return f
}

func planDeploy(cfg *config.Config, dir string) deployPlan {
	var p deployPlan
	proj, _ := config.LoadProject(dir)
	d := &config.Deploy{}
	if proj != nil && proj.Deploy != nil {
		d, p.Deployed = proj.Deploy, true
	}
	p.App, _ = appName(cfg, dir, proj)
	p.Server, _ = resolveServer(cfg, d)
	p.Domain, p.Volumes, p.Release = d.Domain, d.Volumes, d.Release
	plan, err := detect.Build(dir, d.Dockerfile, d.Start)
	if err != nil {
		var e *Error
		if errors.As(err, &e) {
			p.Problem = e.Message + "; " + e.Hint
		} else {
			p.Problem = err.Error()
		}
	} else {
		p.Stack, p.Port, p.Via = plan.Stack, firstNonZero(d.Port, plan.Port), "a generated Dockerfile (`homebase eject` writes it out)"
		if plan.Dockerfile != "" {
			p.Via = plan.Dockerfile
		}
		p.Context, _ = inspectContext(dir, plan, d.Build == "remote")
	}
	switch {
	case p.Server == "":
		p.Next = "ask the user for a server, then `homebase server add user@host`"
	case p.Problem != "":
		p.Next = "add a Dockerfile, or set start = \"...\" under [deploy] in homebase.toml"
	default:
		p.Next = "`homebase deploy --json`, then `homebase status --prod --json`; run `homebase db add` first if the app needs Postgres"
	}
	return p
}
