// homebase manages local dev servers on macOS as launchd agents and routes
// http://<name>.localhost to them through a small reverse proxy.
package main

import (
	"errors"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"text/tabwriter"

	"github.com/semenov/homebase/internal/bonjour"
	"github.com/semenov/homebase/internal/config"
	"github.com/semenov/homebase/internal/launchd"
	"github.com/semenov/homebase/internal/proxy"
)

// version is set at build time: -ldflags "-X main.version=..."
var version = "dev"

const (
	serverLabelPrefix = "dev.homebase.server."
	proxyLabel        = "dev.homebase.proxy"
)

const usage = `homebase — run local dev servers as launchd agents, reachable at http://<name>.localhost

Usage:
  homebase add <name> [-dir DIR] [-port N] [-env K=V]... [-force] -- <command...>
  homebase rm <name>
  homebase ls
  homebase start|stop|restart <name>... | -a
  homebase logs <name> [-f] [-n LINES]
  homebase open <name>
  homebase edit                     open the config in $EDITOR
  homebase lan on|off|status        reach servers from other devices at http://<name>.local
  homebase proxy install|uninstall|status|run
  homebase version

Servers get their port in $PORT. Commands run via "zsh -lc" in DIR,
so your normal shell PATH (nvm, brew, ...) is available.

Config: ` + "%s\n"

func main() {
	if len(os.Args) < 2 {
		fmt.Fprintf(os.Stderr, usage, config.Path())
		os.Exit(2)
	}
	cmd, args := os.Args[1], os.Args[2:]
	var err error
	switch cmd {
	case "add":
		err = cmdAdd(args)
	case "rm", "remove":
		err = cmdRemove(args)
	case "ls", "list", "status":
		err = cmdList()
	case "start", "stop", "restart":
		err = cmdControl(cmd, args)
	case "logs":
		err = cmdLogs(args)
	case "open":
		err = cmdOpen(args)
	case "edit":
		err = cmdEdit()
	case "lan":
		err = cmdLAN(args)
	case "proxy":
		err = cmdProxy(args)
	case "version", "-v", "--version":
		fmt.Println(version)
	case "help", "-h", "--help":
		fmt.Printf(usage, config.Path())
	default:
		err = fmt.Errorf("unknown command %q (see `homebase help`)", cmd)
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "homebase:", err)
		os.Exit(1)
	}
}

type envFlag map[string]string

func (e envFlag) String() string { return "" }
func (e envFlag) Set(v string) error {
	k, val, ok := strings.Cut(v, "=")
	if !ok || k == "" {
		return fmt.Errorf("expected KEY=VALUE, got %q", v)
	}
	e[k] = val
	return nil
}

func cmdAdd(args []string) error {
	if len(args) == 0 || strings.HasPrefix(args[0], "-") {
		return errors.New("usage: homebase add <name> [flags] -- <command...>")
	}
	name := args[0]
	if err := config.ValidName(name); err != nil {
		return err
	}
	fs := flag.NewFlagSet("add", flag.ContinueOnError)
	dir := fs.String("dir", ".", "working directory")
	port := fs.Int("port", 0, "port the server listens on (default: first free from 4000)")
	force := fs.Bool("force", false, "overwrite an existing server")
	env := envFlag{}
	fs.Var(env, "env", "extra environment variable KEY=VALUE (repeatable)")
	if err := fs.Parse(args[1:]); err != nil {
		return err
	}
	command := strings.Join(fs.Args(), " ")
	if command == "" {
		return errors.New("missing command, e.g. homebase add web -- npm run dev")
	}
	absDir, err := filepath.Abs(*dir)
	if err != nil {
		return err
	}
	if fi, err := os.Stat(absDir); err != nil || !fi.IsDir() {
		return fmt.Errorf("not a directory: %s", absDir)
	}

	cfg, err := config.Load()
	if err != nil {
		return err
	}
	if _, exists := cfg.Servers[name]; exists && !*force {
		return fmt.Errorf("%q already exists (use -force to overwrite)", name)
	}
	if *port == 0 {
		*port = cfg.FreePort()
	}
	cfg.Servers[name] = &config.Server{Dir: absDir, Command: command, Port: *port, Env: env}
	if err := cfg.Save(); err != nil {
		return err
	}
	fmt.Printf("added %s → %s (port %d)\nstart it with: homebase start %s\n", name, cfg.URL(name), *port, name)
	if launchd.Get(serverLabelPrefix + name).Loaded {
		fmt.Printf("it is running with the old settings; apply with: homebase restart %s\n", name)
	}
	return nil
}

func cmdRemove(args []string) error {
	if len(args) != 1 {
		return errors.New("usage: homebase rm <name>")
	}
	cfg, err := config.Load()
	if err != nil {
		return err
	}
	name := args[0]
	if _, err := cfg.Get(name); err != nil {
		return err
	}
	if err := launchd.Remove(serverLabelPrefix + name); err != nil {
		return err
	}
	delete(cfg.Servers, name)
	if err := cfg.Save(); err != nil {
		return err
	}
	fmt.Println("removed", name)
	return nil
}

func cmdList() error {
	cfg, err := config.Load()
	if err != nil {
		return err
	}
	tw := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
	lanCol := ""
	if cfg.Proxy.LAN {
		lanCol = "LAN URL\t"
	}
	fmt.Fprintln(tw, "NAME\tSTATE\tPID\tPORT\tURL\t"+lanCol+"DIR")
	for _, name := range cfg.Names() {
		s := cfg.Servers[name]
		st := launchd.Get(serverLabelPrefix + name)
		pid := "-"
		if st.PID > 0 {
			pid = strconv.Itoa(st.PID)
		}
		port := strconv.Itoa(s.Port)
		if st.Running && !config.Listening(s.Port) {
			port += " (closed)"
		}
		lanURL := ""
		if cfg.Proxy.LAN {
			lanURL = cfg.LANURL(name) + "\t"
		}
		fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\t%s%s\n", name, state(st), pid, port, cfg.URL(name), lanURL, tildify(s.Dir))
	}
	tw.Flush()
	if len(cfg.Servers) == 0 {
		fmt.Println("no servers yet — homebase add <name> -- <command>")
	}
	ps := launchd.Get(proxyLabel)
	if !ps.Running {
		fmt.Printf("\nproxy: %s — *.%s URLs won't work until `homebase proxy install`\n", state(ps), cfg.Proxy.Domain)
	}
	return nil
}

func state(st launchd.Status) string {
	switch {
	case st.Running:
		return "running"
	case !st.Loaded:
		return "stopped"
	case st.LastExit != "" && st.LastExit != "0" && !strings.HasPrefix(st.LastExit, "("):
		return "crashed (exit " + st.LastExit + ")"
	default:
		return "exited"
	}
}

func tildify(p string) string {
	home, _ := os.UserHomeDir()
	if rest, ok := strings.CutPrefix(p, home); ok {
		return "~" + rest
	}
	return p
}

func serverJob(name string, s *config.Server) *launchd.Job {
	env := map[string]string{"PORT": strconv.Itoa(s.Port)}
	for k, v := range s.Env {
		env[k] = v
	}
	return &launchd.Job{
		Label:   serverLabelPrefix + name,
		Args:    []string{"/bin/zsh", "-lc", s.Command},
		Dir:     s.Dir,
		Env:     env,
		LogPath: filepath.Join(launchd.LogDir(), name+".log"),
	}
}

func cmdControl(cmd string, args []string) error {
	cfg, err := config.Load()
	if err != nil {
		return err
	}
	names := args
	if len(args) == 1 && (args[0] == "-a" || args[0] == "--all") {
		names = cfg.Names()
	}
	if len(names) == 0 {
		return fmt.Errorf("usage: homebase %s <name>... | -a", cmd)
	}
	for _, name := range names {
		s, err := cfg.Get(name)
		if err != nil {
			return err
		}
		switch cmd {
		case "start", "restart":
			// Start always reloads, so it doubles as restart.
			if err := launchd.Start(serverJob(name, s)); err != nil {
				return fmt.Errorf("%s: %w", name, err)
			}
			fmt.Printf("%sed %s → %s\n", cmd, name, cfg.URL(name))
		case "stop":
			if err := launchd.Stop(serverLabelPrefix + name); err != nil {
				return fmt.Errorf("%s: %w", name, err)
			}
			fmt.Println("stopped", name)
		}
	}
	if cmd != "stop" && !launchd.Get(proxyLabel).Running {
		fmt.Println("note: the proxy is not running — `homebase proxy install` to enable *.localhost URLs")
	}
	return nil
}

func cmdLogs(args []string) error {
	if len(args) == 0 || strings.HasPrefix(args[0], "-") {
		return errors.New("usage: homebase logs <name> [-f] [-n LINES]")
	}
	name := args[0]
	fs := flag.NewFlagSet("logs", flag.ContinueOnError)
	follow := fs.Bool("f", false, "follow")
	lines := fs.Int("n", 50, "number of lines")
	if err := fs.Parse(args[1:]); err != nil {
		return err
	}
	path := filepath.Join(launchd.LogDir(), name+".log")
	if name != "proxy" {
		cfg, err := config.Load()
		if err != nil {
			return err
		}
		if _, err := cfg.Get(name); err != nil {
			return err
		}
	}
	if _, err := os.Stat(path); err != nil {
		return fmt.Errorf("no logs yet for %s (%s)", name, path)
	}
	tailArgs := []string{"-n", strconv.Itoa(*lines)}
	if *follow {
		tailArgs = append(tailArgs, "-F")
	}
	return run("tail", append(tailArgs, path)...)
}

func cmdOpen(args []string) error {
	if len(args) != 1 {
		return errors.New("usage: homebase open <name>")
	}
	cfg, err := config.Load()
	if err != nil {
		return err
	}
	if _, err := cfg.Get(args[0]); err != nil {
		return err
	}
	return run("open", cfg.URL(args[0]))
}

func cmdEdit() error {
	cfg, err := config.Load()
	if err != nil {
		return err
	}
	if _, err := os.Stat(config.Path()); err != nil {
		if err := cfg.Save(); err != nil {
			return err
		}
	}
	editor := os.Getenv("EDITOR")
	if editor == "" {
		editor = "vi"
	}
	if err := run("/bin/sh", "-c", editor+` "$0"`, config.Path()); err != nil {
		return err
	}
	if _, err := config.Load(); err != nil {
		return err
	}
	fmt.Println("saved; running servers pick up changes on `homebase restart <name>`")
	return nil
}

func cmdLAN(args []string) error {
	if len(args) != 1 {
		return errors.New("usage: homebase lan on|off|status")
	}
	cfg, err := config.Load()
	if err != nil {
		return err
	}
	switch args[0] {
	case "on", "off":
		cfg.Proxy.LAN = args[0] == "on"
		if err := cfg.Save(); err != nil {
			return err
		}
		if !cfg.Proxy.LAN {
			fmt.Println("LAN access off — servers are reachable from this Mac only")
			return nil
		}
		fmt.Println("LAN access on — anyone on your network can reach your servers")
	case "status":
		if !cfg.Proxy.LAN {
			fmt.Println("LAN access: off (homebase lan on)")
			return nil
		}
		fmt.Println("LAN access: on")
	default:
		return fmt.Errorf("unknown lan command %q", args[0])
	}

	if ip, err := bonjour.LANAddr(); err != nil {
		fmt.Println("warning:", err)
	} else {
		fmt.Println("announcing *.local at", ip)
	}
	if !launchd.Get(proxyLabel).Running {
		fmt.Println("warning: the proxy is not running — `homebase proxy install`")
	}
	if out, err := exec.Command("/usr/libexec/ApplicationFirewall/socketfilterfw", "--getblockall").Output(); err == nil &&
		strings.Contains(string(out), "enabled") {
		fmt.Println("warning: the macOS firewall blocks all incoming connections (System Settings → Network → Firewall)")
	}
	for _, name := range cfg.Names() {
		fmt.Printf("  %-16s %s\n", name, cfg.LANURL(name))
	}
	return nil
}

func cmdProxy(args []string) error {
	if len(args) != 1 {
		return errors.New("usage: homebase proxy install|uninstall|status|run")
	}
	switch args[0] {
	case "run":
		return proxy.Run()
	case "install":
		exe, err := stableExecutable()
		if err != nil {
			return err
		}
		job := &launchd.Job{
			Label:     proxyLabel,
			Args:      []string{exe, "proxy", "run"},
			LogPath:   filepath.Join(launchd.LogDir(), "proxy.log"),
			KeepAlive: true,
		}
		if p := os.Getenv("HOMEBASE_CONFIG"); p != "" {
			job.Env = map[string]string{"HOMEBASE_CONFIG": p}
		}
		if err := launchd.Start(job); err != nil {
			return err
		}
		fmt.Println("proxy installed and started (logs: homebase logs proxy)")
		return nil
	case "uninstall":
		if err := launchd.Remove(proxyLabel); err != nil {
			return err
		}
		fmt.Println("proxy uninstalled")
		return nil
	case "status":
		cfg, err := config.Load()
		if err != nil {
			return err
		}
		st := launchd.Get(proxyLabel)
		fmt.Printf("proxy: %s", state(st))
		if st.PID > 0 {
			fmt.Printf(" (pid %d)", st.PID)
		}
		fmt.Printf(", port %d, domain *.%s\n", cfg.Proxy.Port, cfg.Proxy.Domain)
		return nil
	}
	return fmt.Errorf("unknown proxy command %q", args[0])
}

// stableExecutable returns a path to this binary that survives upgrades, for
// the proxy launch agent. Homebrew installs into versioned directories and
// links them from its bin dir, so prefer the `homebase` on PATH when it is a
// link to this same binary.
func stableExecutable() (string, error) {
	exe, err := os.Executable()
	if err != nil {
		return "", err
	}
	if exe, err = filepath.EvalSymlinks(exe); err != nil {
		return "", err
	}
	if strings.Contains(exe, "/go-build") {
		return "", errors.New("install homebase first — the launch agent needs a permanent path to the binary")
	}
	if p, err := exec.LookPath("homebase"); err == nil {
		if p, err = filepath.Abs(p); err == nil {
			if resolved, err := filepath.EvalSymlinks(p); err == nil && resolved == exe {
				return p, nil
			}
		}
	}
	return exe, nil
}

func run(name string, args ...string) error {
	c := exec.Command(name, args...)
	c.Stdin, c.Stdout, c.Stderr = os.Stdin, os.Stdout, os.Stderr
	return c.Run()
}
