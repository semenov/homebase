package agent

import (
	"bufio"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/vsemenov/ship/internal/proto"
)

func cmdBuild(args []string) (any, error) {
	fs := newFlags("build")
	tag := fs.String("tag", "", "")
	dockerfile := fs.String("dockerfile", "Dockerfile", "")
	if err := parse(fs, args); err != nil {
		return nil, err
	}
	if !strings.HasPrefix(*tag, "ship/") {
		return nil, proto.Errf(proto.CodeUsage, "", "build needs --tag ship/<app>:<id>")
	}
	// build context arrives as a tar stream on stdin
	cmd := exec.Command("docker", "build", "-t", *tag, "-f", *dockerfile, "-")
	cmd.Stdin = os.Stdin
	cmd.Stdout = os.Stderr
	cmd.Stderr = os.Stderr
	if err := cmd.Run(); err != nil {
		return nil, proto.Errf(proto.CodeBuild, "see the build output above", "docker build failed: %v", err)
	}
	return map[string]string{"image": *tag}, nil
}

func cmdHasImage(args []string) (any, error) {
	fs := newFlags("has-image")
	image := fs.String("image", "", "")
	if err := parse(fs, args); err != nil {
		return nil, err
	}
	return map[string]bool{"exists": imageExists(*image)}, nil
}

func appStatus(a *proto.App) *proto.AppStatus {
	st := &proto.AppStatus{App: *a, State: "missing"}
	if a.Current == nil {
		return st
	}
	if cs, err := inspectContainer(a.Current.Container); err == nil {
		st.State, st.Restarts = cs.Status, cs.Restarts
		st.Healthy = cs.Status == "running" && probe(a.Current.HostPort, a.HealthPath)
	}
	return st
}

func cmdStatus(args []string) (any, error) {
	fs := newFlags("status")
	name := fs.String("app", "", "")
	if err := parse(fs, args); err != nil {
		return nil, err
	}
	a, err := loadApp(*name)
	if err != nil {
		return nil, err
	}
	return appStatus(a), nil
}

func cmdList(args []string) (any, error) {
	cfg, err := readServerConfig()
	if err != nil {
		cfg = &serverConfig{}
	}
	apps, err := listApps()
	if err != nil {
		return nil, err
	}
	out := struct {
		Server *proto.ServerInfo  `json:"server"`
		Apps   []*proto.AppStatus `json:"apps"`
	}{Server: serverInfo(cfg), Apps: []*proto.AppStatus{}}
	for _, a := range apps {
		out.Apps = append(out.Apps, appStatus(a))
	}
	return out, nil
}

func cmdLogs(args []string) error {
	fs := newFlags("logs")
	name := fs.String("app", "", "")
	lines := fs.Int("n", 100, "")
	follow := fs.Bool("f", false, "")
	since := fs.String("since", "", "")
	if err := parse(fs, args); err != nil {
		return err
	}
	a, err := loadApp(*name)
	if err != nil {
		return err
	}
	if a.Current == nil {
		return proto.Errf(proto.CodeNotFound, "", "app %q has no running release", *name)
	}
	dargs := []string{"logs", "--timestamps", "--tail", strconv.Itoa(*lines)}
	if *follow {
		dargs = append(dargs, "-f")
	}
	if *since != "" {
		dargs = append(dargs, "--since", *since)
	}
	cmd := exec.Command("docker", append(dargs, a.Current.Container)...)
	cmd.Stdout, cmd.Stderr = os.Stdout, os.Stdout
	return cmd.Run()
}

func cmdRollback(args []string) (any, error) {
	return redeploy("rollback", args, func(a *proto.App) (*proto.Release, error) {
		if a.Previous == nil || !imageExists(a.Previous.Image) {
			return nil, proto.Errf(proto.CodeNoPrevious, "", "app %q has no previous release to roll back to", a.Name)
		}
		progress("Rolling back to %s", a.Previous.Image)
		return a.Previous, nil
	})
}

func cmdRestart(args []string) (any, error) {
	return redeploy("restart", args, func(a *proto.App) (*proto.Release, error) {
		if a.Current == nil {
			return nil, proto.Errf(proto.CodeNotFound, "", "app %q has no release", a.Name)
		}
		return a.Current, nil
	})
}

// redeploy runs a zero-downtime release of an image the app already has.
func redeploy(name string, args []string, pick func(*proto.App) (*proto.Release, error)) (any, error) {
	fs := newFlags(name)
	app := fs.String("app", "", "")
	timeout := fs.Duration("timeout", 60*time.Second, "")
	if err := parse(fs, args); err != nil {
		return nil, err
	}
	cfg, err := loadServer("")
	if err != nil {
		return nil, err
	}
	a, err := loadApp(*app)
	if err != nil {
		return nil, err
	}
	unlock, err := lockApp(*app)
	if err != nil {
		return nil, err
	}
	defer unlock()
	rel, err := pick(a)
	if err != nil {
		return nil, err
	}
	return release(cfg, a, rel.Image, rel.ContainerPort, *timeout)
}

// env: shipd env list|set|unset --app X [KEY=VALUE | KEY]...
func cmdEnv(args []string) (any, error) {
	if len(args) == 0 {
		return nil, proto.Errf(proto.CodeUsage, "", "usage: env list|set|unset --app NAME ...")
	}
	op := args[0]
	fs := newFlags("env " + op)
	name := fs.String("app", "", "")
	reveal := fs.Bool("reveal", false, "")
	if err := parse(fs, args[1:]); err != nil {
		return nil, err
	}
	if err := checkAppName(*name); err != nil {
		return nil, err
	}
	path, err := ensureEnvFile(*name)
	if err != nil {
		return nil, err
	}
	env, order, err := readEnv(path)
	if err != nil {
		return nil, err
	}
	switch op {
	case "list":
	case "set":
		for _, kv := range fs.Args() {
			k, v, ok := strings.Cut(kv, "=")
			if !ok || k == "" || strings.ContainsAny(k, " \t") {
				return nil, proto.Errf(proto.CodeUsage, "use KEY=VALUE", "invalid assignment %q", kv)
			}
			if strings.ContainsAny(v, "\n\r") {
				return nil, proto.Errf(proto.CodeUsage, "", "value of %s contains a newline, which docker env files do not support", k)
			}
			if _, exists := env[k]; !exists {
				order = append(order, k)
			}
			env[k] = v
		}
	case "unset":
		for _, k := range fs.Args() {
			delete(env, k)
		}
	default:
		return nil, proto.Errf(proto.CodeUsage, "", "unknown env operation %q", op)
	}
	if op != "list" {
		var b strings.Builder
		for _, k := range order {
			if v, ok := env[k]; ok {
				b.WriteString(k + "=" + v + "\n")
			}
		}
		if err := writeFileAtomic(path, []byte(b.String()), 0o600); err != nil {
			return nil, err
		}
	}
	out := map[string]string{}
	for k, v := range env {
		if !*reveal {
			v = "***"
		}
		out[k] = v
	}
	return map[string]any{"app": *name, "env": out}, nil
}

func readEnv(path string) (map[string]string, []string, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, nil, err
	}
	defer f.Close()
	env := map[string]string{}
	var order []string
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 1<<20), 1<<20)
	for sc.Scan() {
		line := sc.Text()
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		k, v, _ := strings.Cut(line, "=")
		if _, ok := env[k]; !ok {
			order = append(order, k)
		}
		env[k] = v
	}
	return env, order, sc.Err()
}

func cmdDestroy(args []string) (any, error) {
	fs := newFlags("destroy")
	name := fs.String("app", "", "")
	if err := parse(fs, args); err != nil {
		return nil, err
	}
	a, err := loadApp(*name)
	if err != nil {
		return nil, err
	}
	unlock, err := lockApp(*name)
	if err != nil {
		return nil, err
	}
	defer unlock()
	progress("Removing containers")
	if ids, _ := output("docker", "ps", "-aq", "--filter", "label=ship.app="+a.Name); strings.TrimSpace(ids) != "" {
		combined("docker", append([]string{"rm", "-f"}, strings.Fields(ids)...)...)
	}
	if cfg, err := readServerConfig(); err == nil {
		progress("Removing nginx config")
		if err := removeNginx(siteConfPath(cfg, a.Name)); err != nil {
			return nil, err
		}
	}
	if a.CertByShip {
		progress("Deleting certificate for %s", a.Domain)
		combined("certbot", "delete", "--cert-name", a.Domain, "--non-interactive")
	}
	progress("Removing images")
	if imgs, _ := output("docker", "images", "ship/"+a.Name, "--format", "{{.Repository}}:{{.Tag}}"); strings.TrimSpace(imgs) != "" {
		combined("docker", append([]string{"rmi"}, strings.Fields(imgs)...)...)
	}
	if err := os.RemoveAll(filepath.Clean(appDir(a.Name))); err != nil {
		return nil, err
	}
	return map[string]any{"app": a.Name, "destroyed": true}, nil
}
