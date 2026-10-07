package cli

import (
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/semenov/homebase/internal/bonjour"
	"github.com/semenov/homebase/internal/config"
	"github.com/semenov/homebase/internal/launchd"
)

const (
	serverLabelPrefix = "dev.homebase.server."
	proxyLabel        = "dev.homebase.proxy"
	tunnelLabel       = "dev.homebase.tunnel"
	tunnelName        = "homebase"
)

// flagApp is the global -a/--app NAME.
var flagApp string

func loadConfig() (*config.Config, error) {
	cfg, err := config.Load()
	if err != nil {
		return nil, errf(CodeConfig, "fix or delete "+short(config.Path()), "%v", err)
	}
	return cfg, nil
}

func saveConfig(cfg *config.Config) error {
	if err := cfg.Save(); err != nil {
		return errf(CodeConfig, "", "save %s: %v", short(config.Path()), err)
	}
	return nil
}

// target resolves the server a command acts on: -a NAME, otherwise the
// server whose folder is the current directory or the nearest parent of it.
func target(cfg *config.Config) (string, *config.Server, error) {
	if flagApp != "" {
		s, ok := cfg.Servers[flagApp]
		if !ok {
			return "", nil, errf(CodeNotFound, "see `homebase ls` for the servers on this Mac", "there is no server named %q", flagApp)
		}
		return flagApp, s, nil
	}
	cwd, err := os.Getwd()
	if err != nil {
		return "", nil, err
	}
	names := serversFor(cfg, cwd)
	switch {
	case len(names) == 1:
		return names[0], cfg.Servers[names[0]], nil
	case len(names) > 1:
		return "", nil, errf(CodeUsage, "pass one with -a NAME", "%s has several servers: %s", short(cwd), strings.Join(names, ", "))
	}
	if dir := config.FindProjectDir(cwd); dir != "" {
		return "", nil, errf(CodeNotFound, "start it with `homebase`", "this project is not running on this Mac yet")
	}
	return "", nil, errf(CodeNotFound, "run `homebase` in a project folder to start it, or pass -a NAME (see `homebase ls`)",
		"no homebase server in %s", short(cwd))
}

// serversFor returns the servers registered for dir or its nearest parent
// that has any.
func serversFor(cfg *config.Config, dir string) []string {
	paths := []string{dir}
	if real, err := filepath.EvalSymlinks(dir); err == nil && real != dir {
		paths = append(paths, real)
	}
	var best []string
	bestLen := -1
	for _, name := range cfg.Names() {
		d := filepath.Clean(cfg.Servers[name].Dir)
		for _, p := range paths {
			if p != d && !strings.HasPrefix(p, d+string(filepath.Separator)) {
				continue
			}
			if len(d) > bestLen {
				best, bestLen = []string{name}, len(d)
			} else if len(d) == bestLen {
				best = append(best, name)
			}
			break
		}
	}
	return best
}

// syncFromProject applies the project's homebase.toml (if any) to the
// registry entry, so edits to the file take effect on the next (re)start.
func syncFromProject(s *config.Server) error {
	p, err := config.LoadProject(s.Dir)
	if err != nil {
		return errf(CodeConfig, "fix the file and try again", "%v", err)
	}
	if p == nil {
		return nil
	}
	if p.Start != "" {
		s.Command = p.Start
	}
	if p.Port != 0 {
		s.Port = p.Port
	}
	s.Env = p.Env
	return nil
}

func logPath(name string) string { return filepath.Join(launchd.LogDir(), name+".log") }

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

// ---- what we report -------------------------------------------------------

type serverInfo struct {
	Name      string            `json:"name"`
	State     string            `json:"state"` // running, stopped, crashed, exited
	LastExit  string            `json:"last_exit,omitempty"`
	PID       int               `json:"pid,omitempty"`
	Uptime    string            `json:"uptime,omitempty"`
	Port      int               `json:"port"`
	Listening bool              `json:"listening"`
	URL       string            `json:"url"` // the best local URL: via the proxy if it runs
	LocalURL  string            `json:"local_url"`
	LANURL    string            `json:"lan_url,omitempty"`
	PublicURL string            `json:"public_url,omitempty"`
	Shared    string            `json:"shared,omitempty"` // public | private
	ShareLink string            `json:"share_link,omitempty"`
	Dir       string            `json:"dir"`
	Command   string            `json:"command"`
	Env       map[string]string `json:"env,omitempty"`
	Log       string            `json:"log"`
}

func stateName(st launchd.Status) string {
	switch {
	case st.Running:
		return "running"
	case !st.Loaded:
		return "stopped"
	case st.LastExit != "" && st.LastExit != "0" && !strings.HasPrefix(st.LastExit, "("):
		return "crashed"
	}
	return "exited"
}

func info(cfg *config.Config, name string) serverInfo {
	s := cfg.Servers[name]
	st := launchd.Get(serverLabelPrefix + name)
	in := serverInfo{
		Name:      name,
		State:     stateName(st),
		PID:       st.PID,
		Port:      s.Port,
		Listening: config.Listening(s.Port),
		LocalURL:  "http://localhost:" + strconv.Itoa(s.Port),
		Dir:       s.Dir,
		Command:   s.Command,
		Env:       s.Env,
		Log:       logPath(name),
	}
	in.URL = in.LocalURL
	if launchd.Get(proxyLabel).Running {
		in.URL = cfg.URL(name)
	}
	if in.State == "crashed" {
		in.LastExit = st.LastExit
	}
	if st.PID > 0 {
		in.Uptime = uptime(st.PID)
	}
	if cfg.Proxy.LAN {
		in.LANURL = cfg.LANURL(name)
	}
	if s.Share != nil && cfg.Tunnel != nil {
		in.PublicURL = cfg.PublicURL(name)
		in.Shared = shareKind(s.Share)
		in.ShareLink = shareLink(cfg, name)
	}
	return in
}

// uptime turns ps's elapsed time ([[dd-]hh:]mm:ss) into "2h 5m".
func uptime(pid int) string {
	out, err := exec.Command("ps", "-o", "etime=", "-p", strconv.Itoa(pid)).Output()
	if err != nil {
		return ""
	}
	e := strings.TrimSpace(string(out))
	days := 0
	if d, rest, ok := strings.Cut(e, "-"); ok {
		days, _ = strconv.Atoi(d)
		e = rest
	}
	parts := strings.Split(e, ":")
	nums := make([]int, 3)
	for i := range parts {
		nums[3-len(parts)+i], _ = strconv.Atoi(parts[i])
	}
	h, m, sec := nums[0]+days*24, nums[1], nums[2]
	switch {
	case h > 0:
		return strconv.Itoa(h) + "h " + strconv.Itoa(m) + "m"
	case m > 0:
		return strconv.Itoa(m) + "m " + strconv.Itoa(sec) + "s"
	}
	return strconv.Itoa(sec) + "s"
}

type machineInfo struct {
	Proxy struct {
		Running bool `json:"running"`
		Port    int  `json:"port"`
	} `json:"proxy"`
	LAN struct {
		On bool   `json:"on"`
		IP string `json:"ip,omitempty"`
	} `json:"lan"`
	Tunnel *struct {
		Running bool   `json:"running"`
		Domain  string `json:"domain"`
	} `json:"tunnel,omitempty"`
}

func machine(cfg *config.Config) machineInfo {
	var m machineInfo
	m.Proxy.Running, m.Proxy.Port = launchd.Get(proxyLabel).Running, cfg.Proxy.Port
	m.LAN.On = cfg.Proxy.LAN
	if m.LAN.On {
		if ip, err := bonjour.LANAddr(); err == nil {
			m.LAN.IP = ip.String()
		}
	}
	if cfg.Tunnel != nil {
		m.Tunnel = &struct {
			Running bool   `json:"running"`
			Domain  string `json:"domain"`
		}{launchd.Get(tunnelLabel).Running, cfg.Tunnel.Domain}
	}
	return m
}

func shareKind(sh *config.Share) string {
	if sh.Public {
		return "public"
	}
	return "private"
}
