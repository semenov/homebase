package cli

import (
	"bufio"
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"strings"

	"github.com/semenov/homebase/internal/agentbin"
	"github.com/semenov/homebase/internal/proto"
)

const agentPath = "/usr/local/bin/shipd"

// Remote talks to one server over the system ssh client, so ~/.ssh/config,
// keys and ssh-agent all work as usual. Connections are multiplexed.
type Remote struct {
	Target string
	Arch   string
	sudo   string
}

func sshArgs(target string, extra ...string) []string {
	args := []string{
		"-o", "BatchMode=yes",
		"-o", "ConnectTimeout=15",
		"-o", "ServerAliveInterval=15",
		"-o", "ControlMaster=auto",
		"-o", "ControlPath=/tmp/homebase-ssh-%C",
		"-o", "ControlPersist=60",
		target,
	}
	return append(args, extra...)
}

func (r *Remote) command(remoteCmd string) *exec.Cmd {
	return exec.Command("ssh", sshArgs(r.Target, remoteCmd)...)
}

func sshError(err error, stderr string) error {
	var ee *exec.ExitError
	if errors.As(err, &ee) && ee.ExitCode() == 255 {
		return &proto.Error{Code: proto.CodeSSH, Message: "ssh connection failed", Hint: "check that `ssh <server>` works without a password prompt", Logs: strings.TrimSpace(stderr)}
	}
	return err
}

// Connect checks the connection and installs or updates shipd, homebase's
// helper on the server, if needed.
func Connect(target string) (*Remote, error) {
	r := &Remote{Target: target}
	var stderr bytes.Buffer
	cmd := r.command("uname -m; id -u; sha256sum " + agentPath + " 2>/dev/null || true")
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		return nil, sshError(err, stderr.String())
	}
	lines := strings.Split(strings.TrimSpace(string(out)), "\n")
	if len(lines) < 2 {
		return nil, proto.Errf(proto.CodeSSH, "", "unexpected response from server: %q", out)
	}
	switch strings.TrimSpace(lines[0]) {
	case "x86_64", "amd64":
		r.Arch = "amd64"
	case "aarch64", "arm64":
		r.Arch = "arm64"
	default:
		return nil, proto.Errf(proto.CodeServerNotReady, "", "unsupported server architecture %q", lines[0])
	}
	if strings.TrimSpace(lines[1]) != "0" {
		r.sudo = "sudo -n "
	}
	bin, err := agentbin.Get(r.Arch)
	if err != nil {
		return nil, &proto.Error{Code: proto.CodeInternal, Message: err.Error()}
	}
	sum := sha256.Sum256(bin)
	if len(lines) >= 3 && strings.HasPrefix(lines[2], hex.EncodeToString(sum[:])) {
		return r, nil
	}
	step("Installing homebase's helper (shipd) on %s", target)
	up := r.command(r.sudo + "sh -c 'cat > " + agentPath + ".new && chmod 755 " + agentPath + ".new && mv " + agentPath + ".new " + agentPath + "'")
	up.Stdin = bytes.NewReader(bin)
	stderr.Reset()
	up.Stderr = &stderr
	if err := up.Run(); err != nil {
		if e := sshError(err, stderr.String()); e != err {
			return nil, e
		}
		return nil, &proto.Error{Code: proto.CodeServerNotReady, Message: "cannot install homebase's helper (shipd) on the server", Hint: "connect as root or a user with passwordless sudo", Logs: stderr.String()}
	}
	return r, nil
}

// Agent runs a shipd command. Progress lines are forwarded to stderr; the
// JSON result is decoded into out (if non-nil).
func (r *Remote) Agent(stdin io.Reader, out any, args ...string) error {
	cmd := r.command(r.sudo + agentPath + " " + shellJoin(args))
	cmd.Stdin = stdin
	var stdout bytes.Buffer
	cmd.Stdout = &stdout
	pr, pw := io.Pipe()
	cmd.Stderr = pw
	done := make(chan string)
	go func() {
		var captured strings.Builder
		sc := bufio.NewScanner(pr)
		sc.Buffer(make([]byte, 64*1024), 1<<20)
		for sc.Scan() {
			line := sc.Text()
			captured.WriteString(line + "\n")
			if !strings.HasPrefix(line, "  ") {
				step("%s", line)
			} else {
				detail("%s", strings.TrimSpace(line))
			}
		}
		io.Copy(io.Discard, pr)
		done <- captured.String()
	}()
	runErr := cmd.Run()
	pw.Close()
	stderr := <-done

	var resp proto.Response
	if err := json.Unmarshal(stdout.Bytes(), &resp); err != nil {
		if runErr != nil {
			return sshError(runErr, stderr)
		}
		return proto.Errf(proto.CodeInternal, "", "invalid response from the server's helper (shipd): %q", stdout.String())
	}
	if !resp.OK {
		return resp.Error
	}
	if out != nil && resp.Data != nil {
		return json.Unmarshal(resp.Data, out)
	}
	return nil
}

// Stream runs a shipd command with stdout/stderr attached to the terminal (used for logs).
func (r *Remote) Stream(args ...string) error {
	cmd := r.command(r.sudo + agentPath + " " + shellJoin(args))
	cmd.Stdout, cmd.Stderr = os.Stdout, os.Stderr
	return cmd.Run()
}

// Interactive runs a shipd command with a terminal allocated (for psql).
func (r *Remote) Interactive(args ...string) error {
	cmd := exec.Command("ssh", sshArgs(r.Target, "-t", r.sudo+agentPath+" "+shellJoin(args))...)
	cmd.Stdin, cmd.Stdout, cmd.Stderr = os.Stdin, os.Stdout, os.Stderr
	return cmd.Run()
}

// Download copies a remote file to a local path.
func (r *Remote) Download(remotePath, localPath string) error {
	f, err := os.OpenFile(localPath, os.O_CREATE|os.O_WRONLY|os.O_EXCL, 0o600)
	if err != nil {
		return err
	}
	cmd := r.command(r.sudo + "cat " + shellQuote(remotePath))
	cmd.Stdout = f
	err = cmd.Run()
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		os.Remove(localPath)
	}
	return err
}

// LoadImage pipes `docker save` output into `docker load` on the server.
func (r *Remote) LoadImage(image string) error {
	save := exec.Command("docker", "save", image)
	load := r.command("gzip -dc | " + r.sudo + "docker load -q")
	pr, pw := io.Pipe()
	save.Stdout = pw
	var saveErr bytes.Buffer
	save.Stderr = &saveErr

	gzipCmd := exec.Command("gzip", "-1")
	gzipCmd.Stdin = pr
	gzOut, err := gzipCmd.StdoutPipe()
	if err != nil {
		return err
	}
	load.Stdin = gzOut
	var loadErr bytes.Buffer
	load.Stderr = &loadErr
	load.Stdout = io.Discard

	if err := save.Start(); err != nil {
		return err
	}
	if err := gzipCmd.Start(); err != nil {
		return err
	}
	if err := load.Start(); err != nil {
		return err
	}
	serr := save.Wait()
	pw.Close()
	gerr := gzipCmd.Wait()
	lerr := load.Wait()
	switch {
	case serr != nil:
		return &proto.Error{Code: proto.CodeUpload, Message: "docker save failed", Logs: saveErr.String()}
	case gerr != nil:
		return &proto.Error{Code: proto.CodeUpload, Message: "gzip failed: " + gerr.Error()}
	case lerr != nil:
		if e := sshError(lerr, loadErr.String()); e != lerr {
			return e
		}
		return &proto.Error{Code: proto.CodeUpload, Message: "docker load on the server failed", Logs: loadErr.String()}
	}
	return nil
}

// PublicIP resolves the server address as seen by DNS (used for sslip.io domains
// and DNS checks). Returns "" if it can't be determined.
func PublicIP(target string) string {
	host := target
	if out, err := exec.Command("ssh", "-G", target).Output(); err == nil {
		for _, line := range strings.Split(string(out), "\n") {
			if h, ok := strings.CutPrefix(line, "hostname "); ok {
				host = strings.TrimSpace(h)
				break
			}
		}
	} else if i := strings.LastIndex(host, "@"); i >= 0 {
		host = host[i+1:]
	}
	ips, err := net.LookupIP(host)
	if err != nil {
		return ""
	}
	for _, ip := range ips {
		if ip4 := ip.To4(); ip4 != nil {
			return ip4.String()
		}
	}
	return ""
}

func shellJoin(args []string) string {
	q := make([]string, len(args))
	for i, a := range args {
		q[i] = shellQuote(a)
	}
	return strings.Join(q, " ")
}

func shellQuote(s string) string {
	if s != "" && strings.IndexFunc(s, func(r rune) bool {
		return !(r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || strings.ContainsRune("-_./:=@,+", r))
	}) < 0 {
		return s
	}
	return "'" + strings.ReplaceAll(s, "'", `'"'"'`) + "'"
}

func humanBytes(n int64) string {
	const unit = 1024
	if n < unit {
		return fmt.Sprintf("%d B", n)
	}
	div, exp := int64(unit), 0
	for m := n / unit; m >= unit; m /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.1f %cB", float64(n)/float64(div), "KMGTPE"[exp])
}
