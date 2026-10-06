// Package launchd manages per-user LaunchAgents through launchctl.
//
// Start enables and bootstraps a job; Stop boots it out and disables it.
// The enabled/disabled state is persisted by launchd, so running servers
// come back after a reboot and stopped ones stay stopped.
package launchd

import (
	"bytes"
	"encoding/xml"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"
)

type Job struct {
	Label   string
	Args    []string
	Dir     string
	Env     map[string]string
	LogPath string
	// KeepAlive restarts the job whenever it exits; otherwise it is only
	// restarted after a crash (non-zero exit).
	KeepAlive bool
}

type Status struct {
	Loaded   bool
	Running  bool
	PID      int
	LastExit string // as reported by launchctl, e.g. "0", "1", "(never exited)"
}

func domain() string { return "gui/" + strconv.Itoa(os.Getuid()) }

func target(label string) string { return domain() + "/" + label }

func PlistPath(label string) string {
	home, _ := os.UserHomeDir()
	return filepath.Join(home, "Library", "LaunchAgents", label+".plist")
}

func LogDir() string {
	home, _ := os.UserHomeDir()
	return filepath.Join(home, "Library", "Logs", "homebase")
}

func esc(s string) string {
	var b bytes.Buffer
	xml.EscapeText(&b, []byte(s))
	return b.String()
}

func (j *Job) plist() []byte {
	var b strings.Builder
	b.WriteString(`<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
`)
	fmt.Fprintf(&b, "\t<key>Label</key>\n\t<string>%s</string>\n", esc(j.Label))
	b.WriteString("\t<key>ProgramArguments</key>\n\t<array>\n")
	for _, a := range j.Args {
		fmt.Fprintf(&b, "\t\t<string>%s</string>\n", esc(a))
	}
	b.WriteString("\t</array>\n")
	if j.Dir != "" {
		fmt.Fprintf(&b, "\t<key>WorkingDirectory</key>\n\t<string>%s</string>\n", esc(j.Dir))
	}
	if len(j.Env) > 0 {
		keys := make([]string, 0, len(j.Env))
		for k := range j.Env {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		b.WriteString("\t<key>EnvironmentVariables</key>\n\t<dict>\n")
		for _, k := range keys {
			fmt.Fprintf(&b, "\t\t<key>%s</key>\n\t\t<string>%s</string>\n", esc(k), esc(j.Env[k]))
		}
		b.WriteString("\t</dict>\n")
	}
	fmt.Fprintf(&b, "\t<key>StandardOutPath</key>\n\t<string>%s</string>\n", esc(j.LogPath))
	fmt.Fprintf(&b, "\t<key>StandardErrorPath</key>\n\t<string>%s</string>\n", esc(j.LogPath))
	b.WriteString("\t<key>RunAtLoad</key>\n\t<true/>\n")
	if j.KeepAlive {
		b.WriteString("\t<key>KeepAlive</key>\n\t<true/>\n")
	} else {
		b.WriteString("\t<key>KeepAlive</key>\n\t<dict>\n\t\t<key>SuccessfulExit</key>\n\t\t<false/>\n\t</dict>\n")
	}
	// Without this launchd applies background CPU/IO throttling.
	b.WriteString("\t<key>ProcessType</key>\n\t<string>Interactive</string>\n")
	b.WriteString("\t<key>ThrottleInterval</key>\n\t<integer>5</integer>\n")
	b.WriteString("</dict>\n</plist>\n")
	return []byte(b.String())
}

func launchctl(args ...string) (string, error) {
	out, err := exec.Command("launchctl", args...).CombinedOutput()
	if err != nil {
		return string(out), fmt.Errorf("launchctl %s: %v: %s", strings.Join(args, " "), err, strings.TrimSpace(string(out)))
	}
	return string(out), nil
}

// Start writes the plist (picking up config changes) and (re)loads the job.
func Start(j *Job) error {
	if err := os.MkdirAll(filepath.Dir(j.LogPath), 0o755); err != nil {
		return err
	}
	if err := os.WriteFile(PlistPath(j.Label), j.plist(), 0o644); err != nil {
		return err
	}
	if err := bootout(j.Label); err != nil {
		return err
	}
	if _, err := launchctl("enable", target(j.Label)); err != nil {
		return err
	}
	_, err := launchctl("bootstrap", domain(), PlistPath(j.Label))
	return err
}

// Stop unloads the job and disables it so it does not start at next login.
func Stop(label string) error {
	if err := bootout(label); err != nil {
		return err
	}
	_, err := launchctl("disable", target(label))
	return err
}

// Remove stops the job and deletes its plist.
func Remove(label string) error {
	if err := Stop(label); err != nil {
		return err
	}
	err := os.Remove(PlistPath(label))
	if os.IsNotExist(err) {
		return nil
	}
	return err
}

// bootout unloads the job if loaded and waits until launchd forgets it;
// bootstrapping again too early fails with "Input/output error".
func bootout(label string) error {
	if !Get(label).Loaded {
		return nil
	}
	if _, err := launchctl("bootout", target(label)); err != nil && Get(label).Loaded {
		return err
	}
	for i := 0; i < 50 && Get(label).Loaded; i++ {
		time.Sleep(100 * time.Millisecond)
	}
	if Get(label).Loaded {
		return fmt.Errorf("%s is still loaded after bootout", label)
	}
	return nil
}

var (
	pidRe   = regexp.MustCompile(`(?m)^\s*pid = (\d+)`)
	stateRe = regexp.MustCompile(`(?m)^\s*state = (\S+)`)
	exitRe  = regexp.MustCompile(`(?m)^\s*last exit code = (.+)$`)
)

func Get(label string) Status {
	out, err := exec.Command("launchctl", "print", target(label)).Output()
	if err != nil {
		return Status{}
	}
	s := Status{Loaded: true}
	text := string(out)
	if m := stateRe.FindStringSubmatch(text); m != nil {
		s.Running = m[1] == "running"
	}
	if m := pidRe.FindStringSubmatch(text); m != nil {
		s.PID, _ = strconv.Atoi(m[1])
	}
	if m := exitRe.FindStringSubmatch(text); m != nil {
		s.LastExit = strings.TrimSpace(m[1])
	}
	return s
}
