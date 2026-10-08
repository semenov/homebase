package cli

import (
	"fmt"
	"net"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/spf13/cobra"

	"github.com/semenov/homebase/internal/config"
	"github.com/semenov/homebase/internal/launchd"
	"github.com/semenov/homebase/internal/proto"
)

var lookupHost = net.LookupHost

// prodResult is the deployed app shown next to the dev server.
type prodResult struct {
	server string
	st     *proto.AppStatus
	err    *Error
}

// statusOut is a dev server plus, when the project is deployed, its app.
type statusOut struct {
	serverInfo
	Prod      *proto.AppStatus `json:"prod,omitempty"`
	ProdError *Error           `json:"prod_error,omitempty"`
}

// showServer emits one server's status. headline is used when it is
// healthy, e.g. "%s is running".
func showServer(cfg *config.Config, name, headline string) {
	showStatus(cfg, name, headline, nil)
}

func showStatus(cfg *config.Config, name, headline string, prod *prodResult) {
	in := info(cfg, name)
	out := statusOut{serverInfo: in}
	if prod != nil {
		out.Prod, out.ProdError = prod.st, prod.err
	}
	emit(out, func(u *UI) {
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
			u.KV("Shared", in.PublicURL+u.p.dim(" ("+in.Shared+")"))
		}
		u.KV("Port", strconv.Itoa(in.Port))
		if in.PID > 0 {
			u.KV("Process", fmt.Sprintf("%d, up %s", in.PID, in.Uptime))
		}
		u.KV("Command", in.Command)
		u.KV("Folder", short(in.Dir))
		u.KV("Logs", short(in.Log))
		if prod != nil {
			prodBlock(u, prod)
		}
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

func prodBlock(u *UI, p *prodResult) {
	u.Head("PROD")
	if p.err != nil {
		u.Bullet(u.p.amber("!"), p.err.Message)
		return
	}
	st := p.st
	state := u.p.green("●") + " live"
	switch {
	case st.Current == nil:
		state = u.p.dim("○ no release")
	case st.State == "running" && !st.Healthy:
		state = u.p.red("✗ unhealthy") + u.p.dim(": "+st.HealthPath+" doesn't answer")
	case st.State != "running":
		state = u.p.red("✗ " + st.State)
	}
	u.KV("State", state)
	prodLines(u, st, p.server)
}

// projectProd fetches the deployed app of the current project. It only asks
// the server when the project was deployed ([deploy] in homebase.toml).
func projectProd() *prodResult {
	_, proj, _, err := currentProject()
	if err != nil || proj == nil || proj.Deploy == nil {
		return nil
	}
	r, st, err := prodStatus()
	res := &prodResult{st: st}
	if r != nil {
		res.server = r.Target
	}
	if err != nil {
		e, ok := err.(*Error)
		if !ok {
			e = errf(CodeInternal, "", "%v", err)
		}
		if e.Code == proto.CodeNotFound {
			e = errf(proto.CodeNotFound, "deploy it with `homebase deploy`", "not deployed on the server")
		}
		res.err = e
	}
	return res
}

func statusCmd() *cobra.Command {
	var local bool
	c := &cobra.Command{
		Use:   "status",
		Short: "This project: the dev server, its share and the deployed app",
		Long: `Shows the dev server of the current folder on this Mac (state, URLs, port, logs)
and, if the project was deployed, the app on your server. --prod shows only the
deployed app; --local only the dev server (no SSH).`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			if flagProd {
				r, st, err := prodStatus()
				if err != nil {
					return err
				}
				showProd(r, st)
				return nil
			}
			cfg, err := loadConfig()
			if err != nil {
				return err
			}
			name, _, terr := target(cfg)
			var prod *prodResult
			if !local && flagApp == "" {
				prod = projectProd()
			}
			switch {
			case terr == nil:
				showStatus(cfg, name, "%s is running", prod)
			case prod == nil:
				return terr
			case prod.err != nil:
				return prod.err
			default:
				showProd(&Remote{Target: prod.server}, prod.st)
			}
			return nil
		},
	}
	c.Flags().BoolVar(&flagProd, "prod", false, "only the deployed app, on the server")
	c.Flags().BoolVar(&local, "local", false, "only the dev server on this Mac")
	return c
}

func listCmd() *cobra.Command {
	var local bool
	c := &cobra.Command{
		Use:     "ls",
		Aliases: []string{"list"},
		Short:   "All dev servers on this Mac, and the apps on your server",
		Args:    cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, err := loadConfig()
			if err != nil {
				return err
			}
			list(cfg, false, !local)
			return nil
		},
	}
	c.Flags().BoolVar(&local, "local", false, "only this Mac (don't ask the server)")
	return c
}

// list prints every dev server and the machine setup, and with withServer the
// apps on the server. With overview it also explains how to get started
// (plain `homebase` outside a project).
func list(cfg *config.Config, overview, withServer bool) {
	servers := []serverInfo{}
	for _, n := range cfg.Names() {
		servers = append(servers, info(cfg, n))
	}
	m := machine(cfg)
	var deployed *serverOverview
	var deployedErr *Error
	if withServer && cfg.Remote != nil {
		var err error
		if deployed, err = fetchServer(cfg.Remote.Host); err != nil {
			if deployedErr, _ = err.(*Error); deployedErr == nil {
				deployedErr = errf(CodeInternal, "", "%v", err)
			}
		}
	}
	data := map[string]any{"servers": servers, "machine": m}
	if deployed != nil {
		data["deployed"] = deployed
	}
	if deployedErr != nil {
		data["deployed_error"] = deployedErr
	}
	emit(data, func(u *UI) {
		if overview {
			u.printf("\n  %s  %s\n", u.p.bold(u.p.fade("⌂ homebase")), u.p.dim("your projects, on your Mac and your server"))
			u.blank = false
		}
		if len(servers) == 0 {
			u.Off("No dev servers yet")
			u.Para("Run homebase in a project folder: it works out how to start the dev server, " +
				"keeps it running in the background and gives it a URL.")
		} else {
			u.Head("DEV SERVERS  " + u.p.dim("this Mac"))
			table(u, servers)
		}
		switch {
		case deployed != nil:
			appTable(u, "DEPLOYED  "+u.p.dim(deployed.Host), deployed.Apps)
		case deployedErr != nil:
			u.Head("DEPLOYED  " + u.p.dim(cfg.Remote.Host))
			u.Bullet(u.p.amber("!"), deployedErr.Message)
		}
		u.Head("SETUP")
		machineLines(u, m)
		switch {
		case !m.Proxy.Running:
			u.Hint("Set up http://<name>.localhost URLs once with", "homebase init")
		case overview || len(servers) == 0:
			u.Hint("Start a project with", "cd my-app && homebase")
		case m.Server == nil:
			u.Hint("Deploy and share through your own server:", "homebase server add user@host")
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
		switch s.Shared {
		case "public":
			extra = "  " + u.p.dim("shared")
		case "private":
			extra = "  " + u.p.dim("shared privately")
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
	if m.Server == nil {
		u.KV("Server", onOff(false, "none"))
		return
	}
	s := m.Server
	ok := s.Shares == 0 || s.Tunnel
	text := s.Host
	if s.DevDomain != "" {
		text += u.p.dim(" · shares https://<name>." + s.DevDomain)
	}
	if !ok {
		text += " " + u.p.amber("(tunnel not running: homebase init)")
	}
	u.KV("Server", onOff(ok, text))
}

func logsCmd() *cobra.Command {
	var follow bool
	var lines int
	var since string
	c := &cobra.Command{
		Use:   "logs [proxy|tunnel]",
		Short: "Show the dev server's output, or with --prod the deployed app's",
		Long: `Shows the last lines of the dev server's log. -f keeps following it.
` + "`homebase logs proxy`" + ` and ` + "`homebase logs tunnel`" + ` show homebase's own services.
With --prod: the deployed app's container logs from the server.`,
		Args:      cobra.MaximumNArgs(1),
		ValidArgs: []string{"proxy", "tunnel"},
		RunE: func(cmd *cobra.Command, args []string) error {
			if flagProd {
				if len(args) > 0 {
					return errf(CodeUsage, "", "--prod shows the deployed app's logs; it takes no argument")
				}
				r, app, err := prodTarget()
				if err != nil {
					return err
				}
				a := []string{"logs", "--app", app, "-n", strconv.Itoa(lines)}
				if follow {
					a = append(a, "-f")
				}
				if since != "" {
					a = append(a, "--since", since)
				}
				if err := r.Stream(a...); err != nil {
					if e := sshError(err, ""); e != err {
						return e
					}
					return proto.Errf(proto.CodeNotFound, "check `homebase status --prod`", "could not read the logs of %q", app)
				}
				return nil
			}
			if since != "" {
				return errf(CodeUsage, "", "--since only works with --prod")
			}
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
	c.Flags().StringVar(&since, "since", "", "with --prod: only lines newer than this, e.g. 10m")
	c.Flags().BoolVar(&flagProd, "prod", false, "the deployed app's logs, from the server")
	return c
}

func openCmd() *cobra.Command {
	c := &cobra.Command{
		Use:   "open",
		Short: "Open the dev server (or with --prod the deployed app) in the browser",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			if flagProd {
				_, st, err := prodStatus()
				if err != nil {
					return err
				}
				return exec.Command("open", st.URL).Run()
			}
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
	c.Flags().BoolVar(&flagProd, "prod", false, "open the deployed app")
	return c
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

// restartLocal restarts a dev server with its current settings.
func restartLocal(name string, s *config.Server, timeout time.Duration) error {
	if err := launchd.Stop(serverLabelPrefix + name); err != nil {
		return errf(CodeLaunchd, "", "stop %s: %v", name, err)
	}
	return start(name, s, timeout)
}

func restartCmd() *cobra.Command {
	var all bool
	var timeout time.Duration
	c := &cobra.Command{
		Use:   "restart",
		Short: "Restart the dev server (re-reads homebase.toml), or with --prod the deployed app",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			if flagProd {
				if all {
					return errf(CodeUsage, "", "--all only works on this Mac")
				}
				return prodRelease("restart", "restarted", max(timeout, 90*time.Second))
			}
			if err := macOnly(); err != nil {
				return err
			}
			var done []serverInfo
			err := eachTarget(all, func(cfg *config.Config, name string, s *config.Server) error {
				if err := syncFromProject(s); err != nil {
					return err
				}
				if err := saveConfig(cfg); err != nil {
					return err
				}
				step("Restarting %s", name)
				if err := restartLocal(name, s, timeout); err != nil {
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
	c.Flags().BoolVar(&all, "all", false, "restart every dev server")
	c.Flags().DurationVar(&timeout, "timeout", 60*time.Second, "how long to wait for the port to open (or the app to become healthy)")
	c.Flags().BoolVar(&flagProd, "prod", false, "restart the deployed app (zero downtime, picks up env changes)")
	return c
}

func stopCmd() *cobra.Command {
	var all bool
	c := &cobra.Command{
		Use:   "stop",
		Short: "Stop the dev server (it stays stopped after a reboot)",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := macOnly(); err != nil {
				return err
			}
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
	c.Flags().BoolVar(&all, "all", false, "stop every dev server")
	return c
}

func forgetCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "forget",
		Short: "Stop the dev server and remove it from homebase (your files stay)",
		Long: `Stops the dev server, removes its launch agent and log, and forgets it. The project
folder and its homebase.toml are not touched: run ` + "`homebase`" + ` there to bring it back.
A deployed app keeps running; ` + "`homebase destroy`" + ` removes that.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := macOnly(); err != nil {
				return err
			}
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
			var unshareErr error
			if s.Share != nil {
				unshareErr = unpublish(cfg, name)
				stopTunnelIfUnused(cfg)
			}
			emit(map[string]any{"forgotten": name}, func(u *UI) {
				u.Off("Forgot %s", name)
				u.Para("Its folder %s is untouched.", short(s.Dir))
				if unshareErr != nil {
					u.Para("Its share route stays on the server (%v); it answers 404.", unshareErr)
				}
			})
			return nil
		},
	}
}
