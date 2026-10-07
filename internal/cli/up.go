package cli

import (
	"errors"
	"fmt"
	"io"
	"maps"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/semenov/homebase/internal/config"
	"github.com/semenov/homebase/internal/detect"
	"github.com/semenov/homebase/internal/launchd"
)

type upOpts struct {
	start   string
	port    int
	env     []string
	noSave  bool
	timeout time.Duration
}

// projectDir picks the folder `homebase` works on: the explicit argument,
// else the nearest folder upwards that is registered or has a homebase.toml
// (so it works from a subfolder), else the current directory.
func projectDir(cfg *config.Config, arg string) (string, error) {
	if arg != "" {
		dir, err := filepath.Abs(arg)
		if err != nil {
			return "", err
		}
		if fi, err := os.Stat(dir); err != nil || !fi.IsDir() {
			return "", errf(CodeUsage, "", "%s is not a folder", arg)
		}
		return dir, nil
	}
	cwd, err := os.Getwd()
	if err != nil {
		return "", err
	}
	best := ""
	if names := serversFor(cfg, cwd); len(names) > 0 {
		best = cfg.Servers[names[0]].Dir
	}
	if p := config.FindProjectDir(cwd); len(p) > len(best) {
		best = p
	}
	if best == "" {
		best = cwd
	}
	return best, nil
}

// isProject reports whether `homebase` with no arguments should start the
// current folder or show the overview instead.
func isProject(cfg *config.Config) bool {
	cwd, _ := os.Getwd()
	return len(serversFor(cfg, cwd)) > 0 || config.FindProjectDir(cwd) != "" ||
		detect.LooksLikeProject(cwd) || detect.Detect(cwd) != nil
}

func runUp(arg string, o upOpts) error {
	cfg, err := loadConfig()
	if err != nil {
		return err
	}
	dir, err := projectDir(cfg, arg)
	if err != nil {
		return err
	}
	proj, err := config.LoadProject(dir)
	if err != nil {
		return errf(CodeConfig, "fix the file and run `homebase` again", "%v", err)
	}
	firstRun := proj == nil
	if proj == nil {
		proj = &config.Project{}
	}

	// Name: -a, the project file, the server already registered for this
	// folder, or the folder name.
	name := flagApp
	if name == "" {
		name = proj.Name
	}
	if name == "" {
		for _, n := range cfg.Names() {
			if cfg.Servers[n].Dir == dir {
				name = n
				break
			}
		}
	}
	if name == "" {
		if name, err = nameFromDir(dir); err != nil {
			return err
		}
	}
	if err := config.ValidName(name); err != nil {
		return errf(CodeUsage, "pass a name with -a NAME", "%v", err)
	}
	old := cfg.Servers[name]
	if old != nil && old.Dir != dir {
		return errf(CodeConfig, "pick another name for this one: homebase -a NAME",
			"a server named %q already runs from %s", name, short(old.Dir))
	}

	// Command: --start, homebase.toml, the registry, or detection.
	command := firstNonEmpty(o.start, proj.Start)
	if command == "" && old != nil {
		command = old.Command
	}
	var found *detect.Result
	if command == "" {
		found = detect.Detect(dir)
		if found == nil {
			return errf(CodeStackUnknown,
				`tell homebase how to start it, listening on $PORT: homebase --start "npm run dev -- --port $PORT"`,
				"don't know how to start the project in %s", short(dir))
		}
		command = found.Command
		line := fmt.Sprintf("Detected %s: %s", found.Stack, command)
		if found.Note != "" {
			line += errPalette.dim("  (" + found.Note + ")")
		}
		step("%s", line)
	} else if d := detect.Detect(dir); d != nil {
		found = &detect.Result{Install: d.Install} // still check dependencies
	}
	if found != nil && found.Install != "" {
		return errf(CodeDepsMissing, "install them first: "+found.Install+", then run `homebase` again",
			"the project's dependencies are not installed")
	}

	port := firstNonZero(o.port, proj.Port)
	if port == 0 && old != nil {
		port = old.Port
	}
	if port == 0 {
		port = cfg.FreePort()
	}
	env := maps.Clone(proj.Env)
	if env == nil && old != nil && firstRun {
		env = maps.Clone(old.Env)
	}
	for _, kv := range o.env {
		k, v, ok := strings.Cut(kv, "=")
		if !ok || k == "" {
			return errf(CodeUsage, "", "--env expects KEY=VALUE, got %q", kv)
		}
		if env == nil {
			env = map[string]string{}
		}
		env[k] = v
	}

	// Remember explicit choices in homebase.toml, so the next run (or a
	// teammate, or an agent) needs no flags.
	if !o.noSave {
		changed := firstRun
		if flagApp != "" && proj.Name != flagApp || proj.Name == "" {
			proj.Name, changed = name, true
		}
		if proj.Start != command {
			proj.Start, changed = command, true
		}
		if o.port != 0 && proj.Port != o.port {
			proj.Port, changed = o.port, true
		}
		if len(o.env) > 0 {
			proj.Env, changed = env, true
		}
		if changed {
			if err := proj.Save(dir); err != nil {
				return errf(CodeConfig, "", "write %s: %v", config.ProjectFile, err)
			}
			verb := "Updated"
			if firstRun {
				verb = "Wrote"
			}
			step("%s %s %s", verb, config.ProjectFile, errPalette.dim("(commit it: anyone can now run `homebase` here)"))
		}
	}

	srv := &config.Server{Dir: dir, Command: command, Port: port, Env: env}
	if old != nil {
		srv.Share = old.Share
	}
	cfg.Servers[name] = srv

	label := serverLabelPrefix + name
	st := launchd.Get(label)
	same := old != nil && old.Command == srv.Command && old.Port == srv.Port && maps.Equal(old.Env, srv.Env)
	if st.Running && same {
		if err := saveConfig(cfg); err != nil {
			return err
		}
		if !config.Listening(port) {
			if err := waitReady(name, srv, time.Now().Add(o.timeout), 0); err != nil {
				return err
			}
		}
		showServer(cfg, name, "%s is already running")
		return nil
	}

	if st.Loaded {
		step("Restarting %s with the new settings", name)
		if err := launchd.Stop(label); err != nil {
			return errf(CodeLaunchd, "", "stop %s: %v", name, err)
		}
	}
	if config.Listening(port) {
		if o.port != 0 || proj.Port != 0 {
			return errf(CodePortInUse, fmt.Sprintf("see what uses it: lsof -nP -iTCP:%d -sTCP:LISTEN, or pick another: homebase --port N", port),
				"port %d is already in use by another program", port)
		}
		// The port wasn't pinned, so any free one will do.
		free := cfg.FreePort()
		step("Port %d is taken by another program, using %d", port, free)
		port, srv.Port = free, free
	}
	if err := saveConfig(cfg); err != nil {
		return err
	}
	if err := start(name, srv, o.timeout); err != nil {
		return err
	}
	showServer(cfg, name, "%s is running")
	return nil
}

// start (re)starts the server and waits until its port accepts connections.
func start(name string, s *config.Server, timeout time.Duration) error {
	var offset int64
	if fi, err := os.Stat(logPath(name)); err == nil {
		offset = fi.Size()
	}
	if err := launchd.Start(serverJob(name, s)); err != nil {
		return errf(CodeLaunchd, "", "start %s: %v", name, err)
	}
	return waitReady(name, s, time.Now().Add(timeout), offset)
}

// waitReady blocks until the server accepts connections on its port. It
// fails early if the process exits, attaching the log output written since
// the start so the cause is visible right away.
func waitReady(name string, s *config.Server, deadline time.Time, logOffset int64) error {
	sp := spin(fmt.Sprintf("Waiting for %s on port %d", name, s.Port))
	defer sp.Stop()
	label := serverLabelPrefix + name
	for {
		if config.Listening(s.Port) {
			return nil
		}
		st := launchd.Get(label)
		var e *Error
		switch {
		case !st.Loaded:
			e = errf(CodeLaunchd, "", "%s is not loaded in launchd", name)
		case !st.Running && stateName(st) == "crashed":
			e = errf(CodeStartFailed, "fix the error above, then run `homebase` again (the command is in homebase.toml)",
				"%s crashed while starting (exit %s)", name, st.LastExit)
		case !st.Running && st.LastExit == "0":
			e = errf(CodeStartFailed, "the command must keep running and serve on $PORT; check `start` in homebase.toml",
				"%s exited without serving anything", name)
		case time.Now().After(deadline):
			e = errf(CodeNotListening, fmt.Sprintf("does the command listen on $PORT (%d)? Check `start` in homebase.toml, or wait longer with --timeout", s.Port),
				"%s is running but port %d never opened", name, s.Port)
		}
		if e != nil {
			sp.Stop()
			e.Logs = readLogSince(logPath(name), logOffset)
			return e
		}
		time.Sleep(250 * time.Millisecond)
	}
}

func readLogSince(path string, offset int64) string {
	const maxBytes = 6 << 10
	f, err := os.Open(path)
	if err != nil {
		return ""
	}
	defer f.Close()
	fi, err := f.Stat()
	if err != nil {
		return ""
	}
	if fi.Size() < offset {
		offset = 0
	}
	if fi.Size()-offset > maxBytes {
		offset = fi.Size() - maxBytes
	}
	data := make([]byte, fi.Size()-offset)
	if _, err := f.ReadAt(data, offset); err != nil && !errors.Is(err, io.EOF) {
		return ""
	}
	return strings.TrimRight(string(data), "\n")
}

var nonNameRe = regexp.MustCompile(`[^a-z0-9]+`)

// nameFromDir turns a project folder into a server name: "My_App" → "my-app".
func nameFromDir(dir string) (string, error) {
	name := strings.Trim(nonNameRe.ReplaceAllString(strings.ToLower(filepath.Base(dir)), "-"), "-")
	if len(name) > 63 {
		name = strings.TrimRight(name[:63], "-")
	}
	if name == "" {
		return "", errf(CodeUsage, "pass one with -a NAME", "can't make a server name from the folder name")
	}
	return name, nil
}

func firstNonEmpty(s ...string) string {
	for _, v := range s {
		if v != "" {
			return v
		}
	}
	return ""
}

func firstNonZero(n ...int) int {
	for _, v := range n {
		if v != 0 {
			return v
		}
	}
	return 0
}
