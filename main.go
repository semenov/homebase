// homebase manages local dev servers on macOS as launchd agents and routes
// http://<name>.localhost to them through a small reverse proxy.
package main

import (
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"text/tabwriter"
	"time"

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
  homebase add [name] [-dir DIR] [-port N] [-env K=V]... [-force] -- <command...>
  homebase rm [name]
  homebase ls [name...] [--json]
  homebase start|restart [name...] | -a [--wait [--timeout 60s]]
  homebase stop [name...] | -a
  homebase logs [name] [-f] [-n LINES]
  homebase open [name]
  homebase edit                     open the config in $EDITOR
  homebase lan on|off|status        reach servers from other devices at http://<name>.local
  homebase proxy install|uninstall|status|run
  homebase tunnel setup <domain> | status | uninstall   optional Cloudflare Tunnel
  homebase share [name] [--private] [--new-token]       publish at https://<name>.<domain>
  homebase unshare [name]
  homebase version
  homebase agents                   usage guide for AI coding agents

Inside a project folder the name can be left out: commands use the server
registered for the current directory, and add names it after the folder.

Servers get their port in $PORT. Commands run via "zsh -lc" in DIR,
so your normal shell PATH (nvm, brew, ...) is available.

AI coding agents: read ` + "`homebase agents`" + ` first.

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
		err = cmdList(args)
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
	case "tunnel":
		err = cmdTunnel(args)
	case "share":
		err = cmdShare(args)
	case "unshare":
		err = cmdUnshare(args)
	case "agents":
		fmt.Print(agentGuide)
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
	name, rest := splitName(args)
	fs := flag.NewFlagSet("add", flag.ContinueOnError)
	dir := fs.String("dir", ".", "working directory")
	port := fs.Int("port", 0, "port the server listens on (default: first free from 4000)")
	force := fs.Bool("force", false, "overwrite an existing server")
	env := envFlag{}
	fs.Var(env, "env", "extra environment variable KEY=VALUE (repeatable)")
	if err := fs.Parse(rest); err != nil {
		return err
	}
	command := strings.Join(fs.Args(), " ")
	if command == "" {
		return errors.New("missing command, e.g. homebase add -- npm run dev")
	}
	absDir, err := filepath.Abs(*dir)
	if err != nil {
		return err
	}
	if fi, err := os.Stat(absDir); err != nil || !fi.IsDir() {
		return fmt.Errorf("not a directory: %s", absDir)
	}
	derived := name == ""
	if derived {
		if name, err = nameFromDir(absDir); err != nil {
			return err
		}
	}
	if err := config.ValidName(name); err != nil {
		return err
	}

	cfg, err := config.Load()
	if err != nil {
		return err
	}
	if old, exists := cfg.Servers[name]; exists && !*force {
		if derived && old.Dir != absDir {
			return fmt.Errorf("a server named %q already exists for %s — pick another name: homebase add <name> -- %s",
				name, tildify(old.Dir), command)
		}
		return fmt.Errorf("%q already exists (use -force to overwrite)", name)
	}
	if *port == 0 {
		*port = cfg.FreePort()
	}
	cfg.Servers[name] = &config.Server{Dir: absDir, Command: command, Port: *port, Env: env}
	if err := cfg.Save(); err != nil {
		return err
	}
	fmt.Printf("added %s → %s (port %d)\nstart it with: homebase start", name, cfg.URL(name), *port)
	if cwd, _ := os.Getwd(); cwd != absDir {
		fmt.Print(" " + name)
	}
	fmt.Println()
	if launchd.Get(serverLabelPrefix + name).Loaded {
		fmt.Printf("it is running with the old settings; apply with: homebase restart %s\n", name)
	}
	return nil
}

func cmdRemove(args []string) error {
	if len(args) > 1 {
		return errors.New("usage: homebase rm [name]")
	}
	cfg, err := config.Load()
	if err != nil {
		return err
	}
	name, _ := splitName(args)
	if name, err = resolveName(cfg, name); err != nil {
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

type serverInfo struct {
	Name      string `json:"name"`
	State     string `json:"state"` // running, stopped, crashed, exited
	LastExit  string `json:"last_exit,omitempty"`
	PID       int    `json:"pid,omitempty"`
	Port      int    `json:"port"`
	Listening bool   `json:"listening"`
	URL       string `json:"url"`
	LocalURL  string `json:"local_url"`
	LANURL    string `json:"lan_url,omitempty"`
	// Shared is "public" or "private" when published through the tunnel;
	// ShareLink includes the access token for private shares.
	Shared    string            `json:"shared,omitempty"`
	PublicURL string            `json:"public_url,omitempty"`
	ShareLink string            `json:"share_link,omitempty"`
	Dir       string            `json:"dir"`
	Command   string            `json:"command"`
	Env       map[string]string `json:"env,omitempty"`
	Log       string            `json:"log"`
}

func cmdList(args []string) error {
	cfg, err := config.Load()
	if err != nil {
		return err
	}
	asJSON := false
	var names []string
	for _, a := range args {
		switch a {
		case "--json", "-json":
			asJSON = true
		default:
			if _, err := cfg.Get(a); err != nil {
				return err
			}
			names = append(names, a)
		}
	}
	if names == nil {
		names = cfg.Names()
	}
	if asJSON {
		return listJSON(cfg, names)
	}
	anyShared := false
	for _, name := range names {
		anyShared = anyShared || cfg.Servers[name].Share != nil
	}
	showPublic := anyShared && cfg.Tunnel != nil
	tw := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
	header := []string{"NAME", "STATE", "PID", "PORT", "URL"}
	if cfg.Proxy.LAN {
		header = append(header, "LAN URL")
	}
	if showPublic {
		header = append(header, "PUBLIC URL")
	}
	fmt.Fprintln(tw, strings.Join(append(header, "DIR"), "\t"))
	for _, name := range names {
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
		row := []string{name, state(st), pid, port, cfg.URL(name)}
		if cfg.Proxy.LAN {
			row = append(row, cfg.LANURL(name))
		}
		if showPublic {
			pub := "-"
			if s.Share != nil {
				pub = cfg.PublicURL(name) + " (" + shareKind(s.Share) + ")"
			}
			row = append(row, pub)
		}
		fmt.Fprintln(tw, strings.Join(append(row, tildify(s.Dir)), "\t"))
	}
	tw.Flush()
	if len(cfg.Servers) == 0 {
		fmt.Println("no servers yet — in a project folder: homebase add -- <command>")
	}
	ps := launchd.Get(proxyLabel)
	if !ps.Running {
		fmt.Printf("\nproxy: %s — *.%s URLs won't work until `homebase proxy install`\n", state(ps), cfg.Proxy.Domain)
	}
	return nil
}

func listJSON(cfg *config.Config, names []string) error {
	ps := launchd.Get(proxyLabel)
	out := struct {
		Config string `json:"config"`
		Proxy  struct {
			Running bool `json:"running"`
			Port    int  `json:"port"`
			LAN     bool `json:"lan"`
		} `json:"proxy"`
		Tunnel *struct {
			Running bool   `json:"running"`
			Domain  string `json:"domain"`
		} `json:"tunnel,omitempty"`
		Servers []serverInfo `json:"servers"`
	}{Config: config.Path(), Servers: []serverInfo{}}
	out.Proxy.Running, out.Proxy.Port, out.Proxy.LAN = ps.Running, cfg.Proxy.Port, cfg.Proxy.LAN
	if cfg.Tunnel != nil {
		out.Tunnel = &struct {
			Running bool   `json:"running"`
			Domain  string `json:"domain"`
		}{launchd.Get(tunnelLabel).Running, cfg.Tunnel.Domain}
	}
	for _, name := range names {
		s := cfg.Servers[name]
		st := launchd.Get(serverLabelPrefix + name)
		info := serverInfo{
			Name:      name,
			State:     stateName(st),
			PID:       st.PID,
			Port:      s.Port,
			Listening: config.Listening(s.Port),
			URL:       cfg.URL(name),
			LocalURL:  "http://localhost:" + strconv.Itoa(s.Port),
			Dir:       s.Dir,
			Command:   s.Command,
			Env:       s.Env,
			Log:       logPath(name),
		}
		if info.State == "crashed" {
			info.LastExit = st.LastExit
		}
		if cfg.Proxy.LAN {
			info.LANURL = cfg.LANURL(name)
		}
		if s.Share != nil && cfg.Tunnel != nil {
			info.Shared = shareKind(s.Share)
			info.PublicURL = cfg.PublicURL(name)
			info.ShareLink = shareLink(cfg, name)
		}
		out.Servers = append(out.Servers, info)
	}
	enc := json.NewEncoder(os.Stdout)
	enc.SetIndent("", "  ")
	return enc.Encode(out)
}

func stateName(st launchd.Status) string {
	switch {
	case st.Running:
		return "running"
	case !st.Loaded:
		return "stopped"
	case st.LastExit != "" && st.LastExit != "0" && !strings.HasPrefix(st.LastExit, "("):
		return "crashed"
	default:
		return "exited"
	}
}

func state(st launchd.Status) string {
	if s := stateName(st); s != "crashed" {
		return s
	}
	return "crashed (exit " + st.LastExit + ")"
}

func logPath(name string) string { return filepath.Join(launchd.LogDir(), name+".log") }

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
		LogPath: logPath(name),
	}
}

func cmdControl(cmd string, args []string) error {
	cfg, err := config.Load()
	if err != nil {
		return err
	}
	var names []string
	all, wait, timeout := false, false, 60*time.Second
	for i := 0; i < len(args); i++ {
		a := args[i]
		switch {
		case a == "-a" || a == "--all":
			all = true
		case (a == "-w" || a == "--wait") && cmd != "stop":
			wait = true
		case strings.HasPrefix(a, "--timeout") && cmd != "stop":
			v, ok := strings.CutPrefix(a, "--timeout=")
			if !ok {
				if i+1 >= len(args) {
					return errors.New("--timeout needs a duration, e.g. 120s")
				}
				i++
				v = args[i]
			}
			if timeout, err = time.ParseDuration(v); err != nil {
				return fmt.Errorf("bad --timeout: %w", err)
			}
			wait = true
		case strings.HasPrefix(a, "-"):
			return fmt.Errorf("unknown flag %s", a)
		default:
			names = append(names, a)
		}
	}
	if all {
		names = cfg.Names()
	} else if len(names) == 0 {
		name, err := serverForCwd(cfg)
		if err != nil {
			return err
		}
		names = []string{name}
	}
	logStart := map[string]int64{}
	for _, name := range names {
		s, err := cfg.Get(name)
		if err != nil {
			return err
		}
		switch cmd {
		case "start", "restart":
			if fi, err := os.Stat(logPath(name)); err == nil {
				logStart[name] = fi.Size()
			}
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
	if wait {
		deadline := time.Now().Add(timeout)
		for _, name := range names {
			if err := waitReady(name, cfg.Servers[name], deadline, logStart[name]); err != nil {
				return err
			}
		}
	}
	return nil
}

// waitReady blocks until the server accepts connections on its port. It
// fails early if the process exits, and prints the log output written
// since the start so the cause is visible without another command.
func waitReady(name string, s *config.Server, deadline time.Time, logOffset int64) error {
	label := serverLabelPrefix + name
	for {
		if config.Listening(s.Port) {
			fmt.Printf("%s is listening on port %d\n", name, s.Port)
			return nil
		}
		st := launchd.Get(label)
		var problem string
		switch {
		case !st.Loaded:
			problem = "is not loaded"
		case !st.Running && stateName(st) == "crashed":
			problem = "crashed (exit " + st.LastExit + ")"
		case !st.Running && st.LastExit == "0":
			problem = "exited with code 0 without listening"
		case time.Now().After(deadline):
			problem = fmt.Sprintf("is running but not listening on port %d yet (does the command use $PORT?)", s.Port)
		}
		if problem != "" {
			printNewLog(name, logOffset)
			return fmt.Errorf("%s %s — log: %s", name, problem, logPath(name))
		}
		time.Sleep(250 * time.Millisecond)
	}
}

func printNewLog(name string, offset int64) {
	const maxBytes = 8 << 10
	f, err := os.Open(logPath(name))
	if err != nil {
		return
	}
	defer f.Close()
	fi, err := f.Stat()
	if err != nil {
		return
	}
	if fi.Size() < offset {
		offset = 0 // truncated or rotated
	}
	if fi.Size()-offset > maxBytes {
		offset = fi.Size() - maxBytes
	}
	data := make([]byte, fi.Size()-offset)
	if _, err := f.ReadAt(data, offset); err != nil && !errors.Is(err, io.EOF) {
		return
	}
	text := strings.TrimRight(string(data), "\n")
	if text == "" {
		fmt.Fprintf(os.Stderr, "--- %s: no log output ---\n", name)
		return
	}
	fmt.Fprintf(os.Stderr, "--- %s log (since start) ---\n%s\n---\n", name, text)
}

func cmdLogs(args []string) error {
	name, rest := splitName(args)
	fs := flag.NewFlagSet("logs", flag.ContinueOnError)
	follow := fs.Bool("f", false, "follow")
	lines := fs.Int("n", 50, "number of lines")
	if err := fs.Parse(rest); err != nil {
		return err
	}
	if name != "proxy" && name != "tunnel" {
		cfg, err := config.Load()
		if err != nil {
			return err
		}
		if name, err = resolveName(cfg, name); err != nil {
			return err
		}
	}
	path := logPath(name)
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
	if len(args) > 1 {
		return errors.New("usage: homebase open [name]")
	}
	cfg, err := config.Load()
	if err != nil {
		return err
	}
	name, _ := splitName(args)
	if name, err = resolveName(cfg, name); err != nil {
		return err
	}
	return run("open", cfg.URL(name))
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
