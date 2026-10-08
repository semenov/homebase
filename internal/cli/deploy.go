package cli

import (
	"archive/tar"
	"compress/gzip"
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/semenov/homebase/internal/config"
	"github.com/semenov/homebase/internal/detect"
	"github.com/semenov/homebase/internal/proto"
)

type deployOpts struct {
	domain      string
	port        int
	health      string
	start       string
	volumes     []string
	release     string
	memory      string
	remoteBuild bool
	noSave      bool
	timeout     time.Duration
}

func deployCmd() *cobra.Command {
	var o deployOpts
	c := &cobra.Command{
		Use:   "deploy [dir]",
		Short: "Build the project and release it on your server (zero downtime)",
		Long: `Builds a Docker image of the project (from its Dockerfile, or one generated for the
detected stack), uploads it to your server over SSH and releases it behind the server's
proxy with HTTPS. The new version must answer HTTP before traffic switches to it; if it
doesn't, the old one keeps serving and the deploy fails with its logs.

The first deploy writes a [deploy] table to homebase.toml. Commit it.`,
		Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error { return runDeploy(firstArg(args), o) },
	}
	f := c.Flags()
	f.StringVar(&o.domain, "domain", "", "domain for the app (its DNS record must point at the server)")
	f.IntVarP(&o.port, "port", "p", 0, "port the app listens on inside the container (default: detected)")
	f.StringVar(&o.health, "health", "", "HTTP path that must answer non-5xx before traffic switches (default /)")
	f.StringVar(&o.start, "start", "", "start command in the image, when there is no Dockerfile")
	f.StringArrayVar(&o.volumes, "volume", nil, "container path to keep across deploys, e.g. /data (repeatable)")
	f.StringVar(&o.memory, "memory", "", `memory limit like "256m" (the app restarts if it exceeds it)`)
	f.StringVar(&o.release, "release", "", `command run in the new image before traffic switches, e.g. "npm run migrate"`)
	f.BoolVar(&o.remoteBuild, "remote-build", false, "build the image on the server instead of locally")
	f.BoolVar(&o.noSave, "no-save", false, "don't write the [deploy] table to homebase.toml")
	f.DurationVar(&o.timeout, "timeout", 90*time.Second, "how long to wait for the app to become healthy")
	return c
}

func runDeploy(arg string, o deployOpts) error {
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
		return errf(CodeConfig, "fix the file and deploy again", "%v", err)
	}
	if proj == nil {
		proj = &config.Project{}
	}
	firstDeploy := proj.Deploy == nil
	d := proj.Deploy
	if d == nil {
		d = &config.Deploy{}
	}
	name, err := appName(cfg, dir, proj)
	if err != nil {
		return err
	}
	server, err := resolveServer(cfg, d)
	if err != nil {
		return err
	}

	plan, err := detect.Build(dir, d.Dockerfile, firstNonEmpty(o.start, d.Start))
	if err != nil {
		return err
	}
	port := firstNonZero(o.port, d.Port, plan.Port)
	step("Deploying %s to %s %s", name, server, errPalette.dim(fmt.Sprintf("(%s, port %d)", plan.Stack, port)))

	r, err := Connect(server)
	if err != nil {
		return err
	}
	tag := fmt.Sprintf("ship/%s:%s", name, time.Now().UTC().Format("20060102-150405"))

	remote := o.remoteBuild || d.Build == "remote"
	if !remote && exec.Command("docker", "info").Run() != nil {
		detail("Docker is not running on this machine, building on the server instead")
		remote = true
	}
	ctxInfo, err := inspectContext(dir, plan, remote)
	if err != nil {
		return err
	}
	what := "Image contents"
	if strings.HasPrefix(plan.Stack, "static") {
		what = "Published files"
	}
	step("%s: %s", what, ctxInfo)
	for _, w := range ctxInfo.Warnings {
		detail("! %s", w)
	}
	if remote {
		err = buildRemote(r, dir, plan, tag)
	} else {
		err = buildLocal(r, dir, plan, tag)
	}
	if err != nil {
		return err
	}

	step("Releasing")
	args := []string{"deploy", "--app", name, "--image", tag, "--port", strconv.Itoa(port),
		"--timeout", o.timeout.String()}
	if ip := PublicIP(server); ip != "" {
		args = append(args, "--ip", ip)
	}
	if v := firstNonEmpty(o.domain, d.Domain); v != "" {
		args = append(args, "--domain", v)
	}
	if v := firstNonEmpty(o.health, d.Health); v != "" {
		args = append(args, "--health", v)
	}
	for _, v := range append(d.Volumes, o.volumes...) {
		args = append(args, "--volume", v)
	}
	if v := firstNonEmpty(o.release, d.Release); v != "" {
		args = append(args, "--release-cmd", v)
	}
	// homebase.toml is the source of truth for the limit: no value removes it
	args = append(args, "--memory", firstNonEmpty(o.memory, d.Memory, "none"))
	var res proto.DeployResult
	if err := r.Agent(nil, &res, args...); err != nil {
		return err
	}

	// The first deploy remembers the server and the flags, like the first
	// `homebase` remembers how to start the dev server.
	if !o.noSave && (firstDeploy || proj.FromShip) {
		if firstDeploy {
			d = &config.Deploy{Server: server, Domain: o.domain, Port: o.port, Health: o.health, Start: o.start,
				Volumes: o.volumes, Release: o.release, Memory: o.memory}
		}
		if proj.Name == "" {
			proj.Name = name
		} else if name != proj.Name {
			d.Name = name
		}
		verb := "Updated"
		if _, err := os.Stat(filepath.Join(dir, config.ProjectFile)); err != nil {
			verb = "Wrote"
		}
		if proj.FromShip {
			verb = "Moved ship.toml into"
		}
		proj.Deploy = d
		if err := proj.Save(dir); err != nil {
			return errf(CodeConfig, "", "write %s: %v", config.ProjectFile, err)
		}
		step("%s %s %s", verb, config.ProjectFile, errPalette.dim("(commit it: next time just run `homebase deploy`)"))
	}

	res.Warnings = append(ctxInfo.Warnings, res.Warnings...)
	out := struct {
		proto.DeployResult
		Context *ContextInfo `json:"context"`
	}{res, ctxInfo}
	emit(out, func(u *UI) {
		u.OK("%s is live", res.App)
		u.KV("URL", res.URL)
		u.KV("Release", res.Release.ID+u.p.dim(" · "+res.Release.Image))
		u.KV("Server", server)
		if len(res.Warnings) > 0 {
			u.gap()
			for _, w := range res.Warnings {
				u.Bullet(u.p.amber("!"), w)
			}
		}
		u.Hint("Check on it with", "homebase status --prod")
	})
	return nil
}

// generatedFiles writes the generated Dockerfile (and a default ignore file if
// the project has none) to a temp dir, returning the Dockerfile path.
func generatedFiles(dir string, plan *detect.Plan) (string, func(), error) {
	tmp, err := os.MkdirTemp("", "homebase-build-")
	if err != nil {
		return "", nil, err
	}
	cleanup := func() { os.RemoveAll(tmp) }
	df := filepath.Join(tmp, "Dockerfile")
	if err := os.WriteFile(df, []byte(plan.Generated), 0o644); err != nil {
		cleanup()
		return "", nil, err
	}
	if _, err := os.Stat(filepath.Join(dir, ".dockerignore")); err != nil {
		// BuildKit reads <Dockerfile>.dockerignore next to the Dockerfile
		os.WriteFile(df+".dockerignore", []byte(plan.Ignore), 0o644)
	}
	return df, cleanup, nil
}

func buildLocal(r *Remote, dir string, plan *detect.Plan, tag string) error {
	df := filepath.Join(dir, plan.Dockerfile)
	if plan.Generated != "" {
		var cleanup func()
		var err error
		if df, cleanup, err = generatedFiles(dir, plan); err != nil {
			return err
		}
		defer cleanup()
	}
	step("Building the image for linux/%s", r.Arch)
	cmd := exec.Command("docker", "buildx", "build", "--platform", "linux/"+r.Arch,
		"-f", df, "-t", tag, "--load", "--progress", "plain", dir)
	var logs strings.Builder
	cmd.Stdout = &logs
	cmd.Stderr = &logs
	if !jsonOut {
		cmd.Stdout = io.MultiWriter(&logs, dimWriter{})
		cmd.Stderr = cmd.Stdout
	}
	if err := cmd.Run(); err != nil {
		return &proto.Error{Code: proto.CodeBuild, Message: "docker build failed", Hint: "fix the build error; run `docker build .` locally to reproduce", Logs: lastLines(logs.String(), 40)}
	}

	size := ""
	if out, err := exec.Command("docker", "image", "inspect", "--format", "{{.Size}}", tag).Output(); err == nil {
		if n, err := strconv.ParseInt(strings.TrimSpace(string(out)), 10, 64); err == nil {
			size = " " + errPalette.dim("("+humanBytes(n)+" uncompressed)")
		}
	}
	step("Uploading the image%s", size)
	err := r.LoadImage(tag)
	exec.Command("docker", "rmi", tag).Run()
	return err
}

func buildRemote(r *Remote, dir string, plan *detect.Plan, tag string) error {
	step("Uploading the source and building on the server")
	dockerfile := plan.Dockerfile
	var extra map[string][]byte
	if plan.Generated != "" {
		dockerfile = ".homebase.Dockerfile"
		extra = map[string][]byte{dockerfile: []byte(plan.Generated)}
	}
	patterns, err := ignorePatterns(dir, plan, true)
	if err != nil {
		return err
	}
	pr, pw := io.Pipe()
	go func() { pw.CloseWithError(writeContext(pw, dir, patterns, extra)) }()
	return r.Agent(pr, nil, "build", "--tag", tag, "--dockerfile", dockerfile)
}

// writeContext writes dir as a gzipped tar, leaving out paths matched by patterns.
func writeContext(w io.Writer, dir string, patterns []string, extra map[string][]byte) error {
	gz := gzip.NewWriter(w)
	tw := tar.NewWriter(gz)
	err := walkContext(dir, patterns, func(rel string, d fs.DirEntry) error {
		fi, err := d.Info()
		if err != nil {
			return err
		}
		link := ""
		if fi.Mode()&fs.ModeSymlink != 0 {
			link, _ = os.Readlink(filepath.Join(dir, rel))
		}
		hdr, err := tar.FileInfoHeader(fi, link)
		if err != nil {
			return nil // sockets etc.
		}
		hdr.Name = rel
		if err := tw.WriteHeader(hdr); err != nil {
			return err
		}
		if fi.Mode().IsRegular() {
			f, err := os.Open(filepath.Join(dir, rel))
			if err != nil {
				return err
			}
			defer f.Close()
			_, err = io.Copy(tw, f)
			return err
		}
		return nil
	}, nil)
	if err != nil {
		return err
	}
	for name, b := range extra {
		tw.WriteHeader(&tar.Header{Name: name, Mode: 0o644, Size: int64(len(b)), ModTime: time.Now()})
		tw.Write(b)
	}
	if err := tw.Close(); err != nil {
		return err
	}
	return gz.Close()
}

func lastLines(s string, n int) string {
	lines := strings.Split(strings.TrimRight(s, "\n"), "\n")
	if len(lines) > n {
		lines = lines[len(lines)-n:]
	}
	return strings.Join(lines, "\n")
}
