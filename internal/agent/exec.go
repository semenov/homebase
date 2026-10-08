package agent

import (
	"fmt"
	"net"
	"net/http"
	"os/exec"
	"strconv"
	"strings"
	"time"
)

func output(name string, args ...string) (string, error) {
	out, err := exec.Command(name, args...).Output()
	return string(out), err
}

func combined(name string, args ...string) (string, error) {
	out, err := exec.Command(name, args...).CombinedOutput()
	return string(out), err
}

func tail(s string, n int) string {
	lines := strings.Split(strings.TrimRight(s, "\n"), "\n")
	if len(lines) > n {
		lines = lines[len(lines)-n:]
	}
	return strings.Join(lines, "\n")
}

type containerState struct {
	Status    string
	Restarts  int
	ExitCode  int
	OOMKilled bool
}

func inspectContainer(name string) (*containerState, error) {
	out, err := output("docker", "inspect", "--format", "{{.State.Status}} {{.RestartCount}} {{.State.ExitCode}} {{.State.OOMKilled}}", name)
	if err != nil {
		return nil, err
	}
	f := strings.Fields(out)
	if len(f) != 4 {
		return nil, fmt.Errorf("unexpected docker inspect output %q", out)
	}
	restarts, _ := strconv.Atoi(f[1])
	code, _ := strconv.Atoi(f[2])
	return &containerState{Status: f[0], Restarts: restarts, ExitCode: code, OOMKilled: f[3] == "true"}, nil
}

func containerLogs(name string, n int) string {
	out, _ := combined("docker", "logs", "--tail", strconv.Itoa(n), name)
	return tail(out, n)
}

func imageExists(image string) bool {
	return exec.Command("docker", "image", "inspect", image).Run() == nil
}

// probe returns true if the app answers HTTP with a non-5xx status.
func probe(port int, path string) bool {
	client := &http.Client{
		Timeout: 3 * time.Second,
		CheckRedirect: func(*http.Request, []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}
	resp, err := client.Get(fmt.Sprintf("http://127.0.0.1:%d%s", port, path))
	if err != nil {
		return false
	}
	resp.Body.Close()
	return resp.StatusCode < 500
}

func portFree(port int) bool {
	l, err := net.Listen("tcp", fmt.Sprintf("127.0.0.1:%d", port))
	if err != nil {
		return false
	}
	l.Close()
	return true
}
