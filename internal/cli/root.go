// Package cli implements the homebase command line.
package cli

import (
	_ "embed"
	"fmt"
	"os"
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
		Short:         "Run local dev servers on your Mac",
		Args:          cobra.MaximumNArgs(1),
		Version:       Version,
		SilenceUsage:  true,
		SilenceErrors: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			if len(args) == 0 && cmd.Flags().NFlag() == 0 {
				cfg, err := loadConfig()
				if err != nil {
					return err
				}
				if !isProject(cfg) {
					list(cfg, true)
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
	pf.StringVarP(&flagApp, "app", "a", "", "server name (default: the server of the current folder)")
	addUpFlags(root, &o)

	startC := &cobra.Command{
		Use:   "start [dir]",
		Short: "Start the project's server (same as plain `homebase`)",
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
		shareCmd(), unshareCmd(), initCmd(), uninstallCmd(), agentsCmd(), docsCmd(), version, proxyCmd())
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
	b.WriteString("\n  " + p.bold(p.fade("⌂ homebase")) + "  " + p.dim(Version) + "\n")
	b.WriteString("  " + p.bold("Local dev servers on your Mac, with real URLs.") + "\n")
	b.WriteString("  " + p.dim("Runs them in the background, restarts them, keeps their logs.") + "\n\n")

	b.WriteString("  " + p.violet("QUICK START") + "\n")
	row("homebase init", "once: http://<name>.localhost URLs for every server", 20)
	row("cd my-app", "", 20)
	row("homebase", "detect, start and give it a URL", 20)
	b.WriteString("\n")

	b.WriteString("  " + p.violet("COMMANDS") + p.dim("  (act on the current folder's server; -a NAME for another)") + "\n")
	for _, c := range []cmdDoc{
		{"homebase [dir]", "start the project here (also: homebase start)"},
		{"status", "state, URLs, port and logs of this project's server"},
		{"ls", "every server on this Mac"},
		{"logs", "its output  (-f to follow, -n for line count)"},
		{"open", "open it in the browser"},
		{"restart", "restart it, re-reading homebase.toml"},
		{"stop", "stop it (stays stopped after a reboot)"},
		{"forget", "stop it and remove it from homebase; files stay"},
		{"share", "publish at https://<name>.<domain>  (--private: token)"},
		{"unshare", "stop publishing it"},
		{"init", "set up this Mac  (--lan, --tunnel DOMAIN)"},
		{"uninstall", "stop everything homebase runs; settings stay"},
		{"agents", "tell your coding agents about homebase"},
		{"docs", "the full guide, plus a plan for this folder"},
	} {
		row(c.name, c.desc, 18)
	}
	b.WriteString("\n  " + p.violet("OPTIONS") + p.dim("  (for homebase / start)") + "\n")
	for _, f := range []cmdDoc{
		{"--start '<cmd>'", "how to run it, listening on $PORT (default: detected)"},
		{"-p, --port N", "use this port"},
		{"-e, --env K=V", "extra environment variable (repeatable)"},
		{"--no-save", "don't write homebase.toml"},
		{"--timeout 60s", "how long to wait for the port"},
	} {
		row(f.name, f.desc, 18)
	}
	b.WriteString("\n  " + p.violet("EVERYWHERE") + "\n")
	row("-a, --app NAME", "act on this server instead of the current folder's", 18)
	row("--json", "one JSON object on stdout, for scripts and agents", 18)

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
			fmt.Printf("    folder          %s\n", short(plan.Dir))
			if plan.Server != "" {
				fmt.Printf("    server          %s (%s, %s)\n", plan.Server, plan.State, plan.URL)
			} else {
				fmt.Printf("    server          none yet\n")
			}
			fmt.Printf("    homebase.toml   %v\n", map[bool]string{true: "yes", false: "no (written on the first run)"}[plan.ProjectFile])
			if plan.Detected != nil {
				fmt.Printf("    detected        %s: %s\n", plan.Detected.Stack, plan.Detected.Command)
				if plan.Detected.Install != "" {
					fmt.Printf("    dependencies    missing: run %s first\n", plan.Detected.Install)
				}
			} else if plan.Server == "" && !plan.ProjectFile {
				fmt.Printf("    detected        nothing: pass --start '<command listening on $PORT>'\n")
			}
			fmt.Printf("    next            %s\n", plan.Next)
			return nil
		},
	}
}

type folder struct {
	Dir         string         `json:"dir"`
	ProjectFile bool           `json:"project_file"`
	Server      string         `json:"server,omitempty"`
	State       string         `json:"state,omitempty"`
	URL         string         `json:"url,omitempty"`
	Detected    *detect.Result `json:"detected,omitempty"`
	Next        string         `json:"next"`
}

func folderPlan(cfg *config.Config) folder {
	cwd, _ := os.Getwd()
	dir, _ := projectDir(cfg, "")
	f := folder{Dir: dir, ProjectFile: config.FindProjectDir(cwd) != ""}
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
	return f
}
