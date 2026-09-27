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

	"github.com/moby/patternmatcher"
	"github.com/moby/patternmatcher/ignorefile"

	"github.com/vsemenov/ship/internal/detect"
	"github.com/vsemenov/ship/internal/proto"
)

type deployOpts struct {
	dir         string
	domain      string
	port        int
	health      string
	start       string
	volumes     []string
	release     string
	remoteBuild bool
	noSave      bool
	timeout     time.Duration
}

func runDeploy(o deployOpts) error {
	dir, err := filepath.Abs(o.dir)
	if err != nil {
		return err
	}
	if fi, err := os.Stat(dir); err != nil || !fi.IsDir() {
		return proto.Errf(proto.CodeConfig, "", "%s is not a directory", o.dir)
	}
	proj, found, err := loadProject(dir)
	if err != nil {
		return err
	}
	name := firstNonEmpty(flagApp, proj.Name, appNameFrom(dir))
	server, err := resolveServer(proj)
	if err != nil {
		return err
	}
	if o.start != "" {
		proj.Start = o.start
	}

	plan, err := detect.Detect(dir, proj.Dockerfile, proj.Start)
	if err != nil {
		return err
	}
	port := plan.Port
	if proj.Port > 0 {
		port = proj.Port
	}
	if o.port > 0 {
		port = o.port
	}
	step("Deploying %s to %s (%s, port %d)", name, server, plan.Stack, port)

	r, err := Connect(server)
	if err != nil {
		return err
	}
	tag := fmt.Sprintf("ship/%s:%s", name, time.Now().UTC().Format("20060102-150405"))

	remote := o.remoteBuild || proj.Build == "remote"
	if !remote && exec.Command("docker", "info").Run() != nil {
		info("Local docker is not running, building on the server instead")
		remote = true
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
	if d := firstNonEmpty(o.domain, proj.Domain); d != "" {
		args = append(args, "--domain", d)
	}
	if h := firstNonEmpty(o.health, proj.Health); h != "" {
		args = append(args, "--health", h)
	}
	for _, v := range append(proj.Volumes, o.volumes...) {
		args = append(args, "--volume", v)
	}
	if rc := firstNonEmpty(o.release, proj.Release); rc != "" {
		args = append(args, "--release-cmd", rc)
	}
	var res proto.DeployResult
	if err := r.Agent(nil, &res, args...); err != nil {
		return err
	}

	saved := ""
	if !found && !o.noSave {
		np := &Project{Name: name, Server: server, Domain: o.domain, Health: o.health, Start: o.start, Volumes: o.volumes, Release: o.release}
		if o.port > 0 {
			np.Port = o.port
		}
		if err := saveProject(dir, np); err == nil {
			saved = filepath.Join(dir, projectFile)
		}
	}
	emit(res, func() {
		fmt.Printf("✓ %s is live at %s\n", res.App, res.URL)
		fmt.Printf("  release %s · %s\n", res.Release.ID, res.Release.Image)
		for _, w := range res.Warnings {
			fmt.Printf("  ! %s\n", w)
		}
		if saved != "" {
			fmt.Printf("  saved %s (commit it; next time just run `ship`)\n", projectFile)
		}
	})
	return nil
}

func firstNonEmpty(s ...string) string {
	for _, v := range s {
		if v != "" {
			return v
		}
	}
	return ""
}

// generatedFiles writes the generated Dockerfile (and a default ignore file if
// the project has none) to a temp dir, returning the Dockerfile path.
func generatedFiles(dir string, plan *detect.Plan) (string, func(), error) {
	tmp, err := os.MkdirTemp("", "ship-build-")
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
		os.WriteFile(df+".dockerignore", []byte(detect.DefaultIgnore), 0o644)
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
	step("Building image for linux/%s", r.Arch)
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
			size = " (" + humanBytes(n) + " uncompressed)"
		}
	}
	step("Uploading image%s", size)
	err := r.LoadImage(tag)
	exec.Command("docker", "rmi", tag).Run()
	return err
}

func buildRemote(r *Remote, dir string, plan *detect.Plan, tag string) error {
	step("Uploading source and building on the server")
	dockerfile := plan.Dockerfile
	var extra map[string][]byte
	if plan.Generated != "" {
		dockerfile = ".ship.Dockerfile"
		extra = map[string][]byte{dockerfile: []byte(plan.Generated)}
	}
	pr, pw := io.Pipe()
	go func() { pw.CloseWithError(writeContext(pw, dir, extra)) }()
	return r.Agent(pr, nil, "build", "--tag", tag, "--dockerfile", dockerfile)
}

// writeContext writes dir as a gzipped tar, honoring .dockerignore.
func writeContext(w io.Writer, dir string, extra map[string][]byte) error {
	patterns := strings.Split(strings.TrimSpace(detect.DefaultIgnore), "\n")
	if f, err := os.Open(filepath.Join(dir, ".dockerignore")); err == nil {
		patterns, err = ignorefile.ReadAll(f)
		f.Close()
		if err != nil {
			return err
		}
	}
	pm, err := patternmatcher.New(patterns)
	if err != nil {
		return err
	}
	gz := gzip.NewWriter(w)
	tw := tar.NewWriter(gz)
	err = filepath.WalkDir(dir, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(dir, path)
		if rel == "." {
			return nil
		}
		rel = filepath.ToSlash(rel)
		if skip, _ := pm.MatchesOrParentMatches(rel); skip {
			if d.IsDir() && !pm.Exclusions() {
				return filepath.SkipDir
			}
			return nil
		}
		fi, err := d.Info()
		if err != nil {
			return err
		}
		link := ""
		if fi.Mode()&fs.ModeSymlink != 0 {
			link, _ = os.Readlink(path)
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
			f, err := os.Open(path)
			if err != nil {
				return err
			}
			defer f.Close()
			_, err = io.Copy(tw, f)
			return err
		}
		return nil
	})
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

// dimWriter shows build output greyed out so ship's own steps stand out.
type dimWriter struct{}

func (dimWriter) Write(p []byte) (int, error) {
	os.Stderr.Write([]byte("\x1b[2m"))
	os.Stderr.Write(p)
	os.Stderr.Write([]byte("\x1b[0m"))
	return len(p), nil
}
