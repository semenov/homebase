package cli

import (
	"fmt"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/spf13/cobra"

	"github.com/semenov/homebase/internal/config"
	"github.com/semenov/homebase/internal/launchd"
)

// showServer emits one server's status. headline is used when it is
// healthy, e.g. "%s is running".
func showServer(cfg *config.Config, name, headline string) {
	in := info(cfg, name)
	emit(in, func(u *UI) {
		switch {
		case in.State == "running" && in.Listening:
			u.OK(headline, name)
		case in.State == "running":
			u.Warn("%s is running, but nothing listens on port %d", name, in.Port)
		case in.State == "crashed":
			u.Bad("%s crashed (exit %s)", name, in.LastExit)
		default:
			u.Off("%s is stopped", name)
		}
		u.KV("URL", in.URL)
		if in.LANURL != "" {
			u.KV("Network", in.LANURL)
		}
		if in.PublicURL != "" {
			u.KV("Public", in.PublicURL+u.p.dim(" ("+in.Shared+")"))
		}
		u.KV("Port", strconv.Itoa(in.Port))
		if in.PID > 0 {
			u.KV("Process", fmt.Sprintf("%d, up %s", in.PID, in.Uptime))
		}
		u.KV("Command", in.Command)
		u.KV("Folder", short(in.Dir))
		u.KV("Logs", short(in.Log))
		switch {
		case in.State == "running" && in.Listening:
			if in.URL == in.LocalURL {
				u.Hint("For http://"+name+".localhost URLs, set up the proxy once:", "homebase init")
			}
		case in.State == "running":
			u.Hint("See what it is doing with", "homebase logs")
		case in.State == "crashed":
			u.Hint("See why with", "homebase logs")
		default:
			u.Hint("Start it with", "homebase")
		}
	})
}

func statusCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "status",
		Short: "Show this project's server: state, URLs, port, logs",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, err := loadConfig()
			if err != nil {
				return err
			}
			name, _, err := target(cfg)
			if err != nil {
				return err
			}
			showServer(cfg, name, "%s is running")
			return nil
		},
	}
}

func listCmd() *cobra.Command {
	return &cobra.Command{
		Use:     "ls",
		Aliases: []string{"list"},
		Short:   "List all servers on this Mac",
		Args:    cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, err := loadConfig()
			if err != nil {
				return err
			}
			list(cfg, false)
			return nil
		},
	}
}

// list prints every server and the machine setup. With overview it also
// explains how to get started (plain `homebase` outside a project).
func list(cfg *config.Config, overview bool) {
	servers := []serverInfo{}
	for _, n := range cfg.Names() {
		servers = append(servers, info(cfg, n))
	}
	m := machine(cfg)
	emit(map[string]any{"servers": servers, "machine": m}, func(u *UI) {
		if overview {
			u.printf("\n  %s  %s\n", u.p.bold(u.p.fade("⌂ homebase")), u.p.dim("local dev servers on your Mac"))
			u.blank = false
		}
		if len(servers) == 0 {
			u.Off("No servers yet")
			u.Para("Run homebase in a project folder: it works out how to start the dev server, " +
				"keeps it running in the background and gives it a URL.")
		} else {
			u.Head("SERVERS")
			table(u, servers)
		}
		u.Head("THIS MAC")
		machineLines(u, m)
		switch {
		case !m.Proxy.Running:
			u.Hint("Set up http://<name>.localhost URLs once with", "homebase init")
		case overview || len(servers) == 0:
			u.Hint("Start a project with", "cd my-app && homebase")
		}
	})
}

func table(u *UI, servers []serverInfo) {
	nameW, urlW := 0, 0
	for _, s := range servers {
		nameW = max(nameW, len(s.Name))
		urlW = max(urlW, utf8.RuneCountInString(s.URL))
	}
	for _, s := range servers {
		mark, url := u.p.green("●"), u.p.blue(s.URL)
		switch {
		case s.State == "crashed":
			mark, url = u.p.red("✗"), u.p.red("crashed (exit "+s.LastExit+")")
		case s.State != "running":
			mark, url = u.p.dim("○"), u.p.dim("stopped")
		case !s.Listening:
			mark = u.p.amber("!")
		}
		plainURL := s.URL
		if s.State != "running" {
			plainURL = map[bool]string{true: "crashed (exit " + s.LastExit + ")", false: "stopped"}[s.State == "crashed"]
		}
		extra := ""
		if s.PublicURL != "" {
			extra = "  " + u.p.dim("public "+s.Shared)
		}
		u.Line(fmt.Sprintf("    %s %s%s  %s%s  %s%s", mark, s.Name, strings.Repeat(" ", nameW-len(s.Name)),
			url, strings.Repeat(" ", max(0, urlW-utf8.RuneCountInString(plainURL))), u.p.dim(short(s.Dir)), extra))
	}
}

func machineLines(u *UI, m machineInfo) {
	onOff := func(on bool, text string) string {
		if on {
			return u.p.green("●") + " " + text
		}
		return u.p.dim("○ " + text)
	}
	proxy := "not set up"
	if m.Proxy.Running {
		proxy = "http://<name>.localhost"
	}
	u.KV("Proxy", onOff(m.Proxy.Running, proxy))
	lan := "off"
	if m.LAN.On {
		lan = "http://<name>.local"
		if m.LAN.IP != "" {
			lan += u.p.dim(" on " + m.LAN.IP)
		}
	}
	u.KV("Network", onOff(m.LAN.On, lan))
	tunnel := "off"
	if m.Tunnel != nil {
		tunnel = "https://<name>." + m.Tunnel.Domain
		if !m.Tunnel.Running {
			tunnel += " (not running)"
		}
	}
	u.KV("Tunnel", onOff(m.Tunnel != nil && m.Tunnel.Running, tunnel))
}

func logsCmd() *cobra.Command {
	var follow bool
	var lines int
	c := &cobra.Command{
		Use:   "logs [proxy|tunnel]",
		Short: "Show the server's output (stdout and stderr)",
		Long: `Shows the last lines of the server's log. -f keeps following it.
` + "`homebase logs proxy`" + ` and ` + "`homebase logs tunnel`" + ` show homebase's own services.`,
		Args:      cobra.MaximumNArgs(1),
		ValidArgs: []string{"proxy", "tunnel"},
		RunE: func(cmd *cobra.Command, args []string) error {
			name := ""
			if len(args) == 1 {
				if args[0] != "proxy" && args[0] != "tunnel" {
					return errf(CodeUsage, "for a server, run it in the project folder or pass -a NAME", "logs takes only `proxy` or `tunnel`")
				}
				name = args[0]
			} else {
				cfg, err := loadConfig()
				if err != nil {
					return err
				}
				if name, _, err = target(cfg); err != nil {
					return err
				}
			}
			path := logPath(name)
			if _, err := os.Stat(path); err != nil {
				return errf(CodeNotFound, "", "%s has no log yet (%s)", name, short(path))
			}
			targs := []string{"-n", strconv.Itoa(lines)}
			if follow {
				targs = append(targs, "-F")
			}
			t := exec.Command("tail", append(targs, path)...)
			t.Stdout, t.Stderr = os.Stdout, os.Stderr
			return t.Run()
		},
	}
	c.Flags().BoolVarP(&follow, "follow", "f", false, "keep printing new lines (never returns)")
	c.Flags().IntVarP(&lines, "lines", "n", 50, "how many lines to show")
	return c
}

func openCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "open",
		Short: "Open the server in the browser",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, err := loadConfig()
			if err != nil {
				return err
			}
			name, _, err := target(cfg)
			if err != nil {
				return err
			}
			return exec.Command("open", info(cfg, name).URL).Run()
		},
	}
}

// eachTarget runs fn for the current server, or every server with --all.
func eachTarget(all bool, fn func(cfg *config.Config, name string, s *config.Server) error) error {
	cfg, err := loadConfig()
	if err != nil {
		return err
	}
	names := cfg.Names()
	if !all {
		name, _, err := target(cfg)
		if err != nil {
			return err
		}
		names = []string{name}
	}
	for _, n := range names {
		if err := fn(cfg, n, cfg.Servers[n]); err != nil {
			return err
		}
	}
	return nil
}

func restartCmd() *cobra.Command {
	var all bool
	var timeout time.Duration
	c := &cobra.Command{
		Use:   "restart",
		Short: "Restart the server (re-reads homebase.toml)",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			var done []serverInfo
			err := eachTarget(all, func(cfg *config.Config, name string, s *config.Server) error {
				if err := syncFromProject(s); err != nil {
					return err
				}
				if err := saveConfig(cfg); err != nil {
					return err
				}
				step("Restarting %s", name)
				if err := launchd.Stop(serverLabelPrefix + name); err != nil {
					return errf(CodeLaunchd, "", "stop %s: %v", name, err)
				}
				if err := start(name, s, timeout); err != nil {
					return err
				}
				done = append(done, info(cfg, name))
				return nil
			})
			if err != nil {
				return err
			}
			if len(done) == 1 {
				cfg, _ := loadConfig()
				showServer(cfg, done[0].Name, "%s restarted")
				return nil
			}
			emit(map[string]any{"servers": done}, func(u *UI) {
				u.OK("Restarted %d servers", len(done))
				table(u, done)
			})
			return nil
		},
	}
	c.Flags().BoolVar(&all, "all", false, "restart every server")
	c.Flags().DurationVar(&timeout, "timeout", 60*time.Second, "how long to wait for the port to open")
	return c
}

func stopCmd() *cobra.Command {
	var all bool
	c := &cobra.Command{
		Use:   "stop",
		Short: "Stop the server (it stays stopped after a reboot)",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			var stopped []string
			err := eachTarget(all, func(cfg *config.Config, name string, s *config.Server) error {
				if err := launchd.Stop(serverLabelPrefix + name); err != nil {
					return errf(CodeLaunchd, "", "stop %s: %v", name, err)
				}
				stopped = append(stopped, name)
				return nil
			})
			if err != nil {
				return err
			}
			emit(map[string]any{"stopped": stopped}, func(u *UI) {
				if len(stopped) == 1 {
					u.Off("%s is stopped", stopped[0])
					u.Hint("Start it again with", "homebase")
					return
				}
				u.Off("Stopped %d servers", len(stopped))
			})
			return nil
		},
	}
	c.Flags().BoolVar(&all, "all", false, "stop every server")
	return c
}

func forgetCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "forget",
		Short: "Stop the server and remove it from homebase (your files stay)",
		Long: `Stops the server, removes its launch agent and log, and forgets it. The project
folder and its homebase.toml are not touched: run ` + "`homebase`" + ` there to bring it back.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, err := loadConfig()
			if err != nil {
				return err
			}
			name, s, err := target(cfg)
			if err != nil {
				return err
			}
			if err := launchd.Remove(serverLabelPrefix + name); err != nil {
				return errf(CodeLaunchd, "", "remove %s: %v", name, err)
			}
			os.Remove(logPath(name))
			delete(cfg.Servers, name)
			if err := saveConfig(cfg); err != nil {
				return err
			}
			emit(map[string]any{"forgotten": name}, func(u *UI) {
				u.Off("Forgot %s", name)
				u.Para("Its folder %s is untouched.", short(s.Dir))
				if s.Share != nil && cfg.Tunnel != nil {
					u.Para("%s now returns 404. Its DNS record stays in Cloudflare; delete it in the dashboard if you like.", cfg.PublicURL(name))
				}
			})
			return nil
		},
	}
}
