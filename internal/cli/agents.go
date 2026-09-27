package cli

import (
	_ "embed"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/spf13/cobra"

	"github.com/semenov/ship/internal/proto"
)

//go:embed skill.md
var skillDoc string

const (
	blockStart = "<!-- ship:start -->"
	blockEnd   = "<!-- ship:end -->"
	skillMark  = "managed by `ship agents`"
)

// agentBlock goes into agents' global instruction files. It stays short and
// defers to `ship docs`, so it does not go stale when ship is updated.
const agentBlock = blockStart + `
## Deploying web apps: use ship

To deploy a web app to the user's own server, use the ` + "`ship`" + ` CLI instead of manual
ssh/docker/nginx work, also when the user only names a server ("deploy to root@1.2.3.4").
Run ` + "`ship docs`" + ` first: it is the full guide plus a plan for the current project.
Deploy with ` + "`ship --json`" + `, verify with ` + "`ship status --json`" + `.
Never run ` + "`ship destroy --data`" + ` unless the user explicitly asks to delete data.
` + blockEnd + "\n"

// agentTarget is one coding agent whose global instructions ship can extend.
type agentTarget struct {
	Name   string `json:"name"`
	Dir    string `json:"-"` // presence means the agent is installed
	Path   string `json:"path"`
	Skill  bool   `json:"-"` // Claude Code skill file instead of an instructions block
	Found  bool   `json:"detected"`
	Status string `json:"status"` // installed | outdated | not_installed | not_detected
}

func agentTargets() []*agentTarget {
	home, _ := os.UserHomeDir()
	cfg := os.Getenv("XDG_CONFIG_HOME")
	if cfg == "" {
		cfg = filepath.Join(home, ".config")
	}
	targets := []*agentTarget{
		{Name: "claude", Dir: filepath.Join(home, ".claude"), Path: filepath.Join(home, ".claude", "skills", "ship", "SKILL.md"), Skill: true},
		{Name: "codex", Dir: filepath.Join(home, ".codex"), Path: filepath.Join(home, ".codex", "AGENTS.md")},
		{Name: "opencode", Dir: filepath.Join(cfg, "opencode"), Path: filepath.Join(cfg, "opencode", "AGENTS.md")},
		{Name: "gemini", Dir: filepath.Join(home, ".gemini"), Path: filepath.Join(home, ".gemini", "GEMINI.md")},
	}
	for _, t := range targets {
		t.Found = isDir(t.Dir)
		t.Status = t.check()
	}
	return targets
}

func isDir(p string) bool {
	fi, err := os.Stat(p)
	return err == nil && fi.IsDir()
}

func (t *agentTarget) check() string {
	if !t.Found {
		return "not_detected"
	}
	b, err := os.ReadFile(t.Path)
	if err != nil {
		return "not_installed"
	}
	s := string(b)
	if t.Skill {
		switch {
		case s == skillDoc:
			return "installed"
		case strings.Contains(s, skillMark):
			return "outdated"
		}
		return "not_installed"
	}
	switch block, ok := findBlock(s); {
	case !ok:
		return "not_installed"
	case block == agentBlock:
		return "installed"
	}
	return "outdated"
}

// findBlock returns ship's marked block (including a trailing newline) if present.
func findBlock(s string) (string, bool) {
	i := strings.Index(s, blockStart)
	if i < 0 {
		return "", false
	}
	j := strings.Index(s[i:], blockEnd)
	if j < 0 {
		return "", false
	}
	end := i + j + len(blockEnd)
	if end < len(s) && s[end] == '\n' {
		end++
	}
	return s[i:end], true
}

func (t *agentTarget) install() error {
	if err := os.MkdirAll(filepath.Dir(t.Path), 0o755); err != nil {
		return err
	}
	b, err := os.ReadFile(t.Path)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	s := string(b)
	if t.Skill {
		if s != "" && !strings.Contains(s, skillMark) {
			return fmt.Errorf("%s exists and was not written by ship; leaving it alone", t.Path)
		}
		return os.WriteFile(t.Path, []byte(skillDoc), 0o644)
	}
	if block, ok := findBlock(s); ok {
		s = strings.Replace(s, block, agentBlock, 1)
	} else {
		if s != "" {
			s = strings.TrimRight(s, "\n") + "\n\n"
		}
		s += agentBlock
	}
	return os.WriteFile(t.Path, []byte(s), 0o644)
}

func (t *agentTarget) uninstall() error {
	b, err := os.ReadFile(t.Path)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	s := string(b)
	if t.Skill {
		if !strings.Contains(s, skillMark) {
			return nil
		}
		return os.RemoveAll(filepath.Dir(t.Path))
	}
	block, ok := findBlock(s)
	if !ok {
		return nil
	}
	s = strings.TrimSpace(strings.Replace(s, block, "", 1))
	if s == "" {
		return os.Remove(t.Path)
	}
	return os.WriteFile(t.Path, []byte(s+"\n"), 0o644)
}

func agentsCmd() *cobra.Command {
	c := &cobra.Command{
		Use:   "agents",
		Short: "Teach coding agents on this machine (Claude Code, Codex, ...) to deploy with ship",
		Long: `Adds a short note to the global instructions of the coding agents installed on
this machine, so that any new agent session knows to deploy with ship and to
start with ` + "`ship docs`" + `. Supported: Claude Code (as a skill), Codex, OpenCode, Gemini CLI.

  ship agents status               where ship is registered
  ship agents install [agent...]   register with all detected agents (or the named ones)
  ship agents uninstall [agent...] remove ship's note again`,
	}
	selectTargets := func(names []string) ([]*agentTarget, error) {
		all := agentTargets()
		if len(names) == 0 {
			return all, nil
		}
		var out []*agentTarget
		for _, n := range names {
			i := slices.IndexFunc(all, func(t *agentTarget) bool { return t.Name == n })
			if i < 0 {
				return nil, proto.Errf(proto.CodeUsage, "known agents: claude, codex, opencode, gemini", "unknown agent %q", n)
			}
			out = append(out, all[i])
		}
		return out, nil
	}
	report := func(targets []*agentTarget, verb string) {
		emit(map[string]any{"agents": targets}, func() {
			for _, t := range targets {
				mark := map[string]string{"installed": "✓", "outdated": "!", "not_installed": "·", "not_detected": " "}[t.Status]
				detail := t.Path
				if !t.Found {
					detail = "not installed on this machine"
				}
				fmt.Printf("%s %-9s %-14s %s\n", mark, t.Name, strings.ReplaceAll(t.Status, "_", " "), detail)
			}
			if verb != "" {
				fmt.Println(verb)
			}
		})
	}

	status := &cobra.Command{
		Use:   "status",
		Short: "Show which agents know about ship",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			report(agentTargets(), "")
			return nil
		},
	}
	install := &cobra.Command{
		Use:   "install [agent...]",
		Short: "Register ship with detected agents (idempotent, also updates old notes)",
		RunE: func(cmd *cobra.Command, args []string) error {
			targets, err := selectTargets(args)
			if err != nil {
				return err
			}
			var done []*agentTarget
			for _, t := range targets {
				if !t.Found {
					if len(args) > 0 {
						return proto.Errf(proto.CodeConfig, "", "%s is not installed on this machine (no %s)", t.Name, t.Dir)
					}
					continue
				}
				if err := t.install(); err != nil {
					return proto.Errf(proto.CodeConfig, "", "%s: %v", t.Name, err)
				}
				t.Status = t.check()
				done = append(done, t)
			}
			if len(done) == 0 {
				return proto.Errf(proto.CodeConfig, "supported: Claude Code, Codex, OpenCode, Gemini CLI", "no supported coding agents found on this machine")
			}
			report(done, "New agent sessions will now deploy with ship. Undo with `ship agents uninstall`.")
			return nil
		},
	}
	uninstall := &cobra.Command{
		Use:   "uninstall [agent...]",
		Short: "Remove ship's note from agents' instructions",
		RunE: func(cmd *cobra.Command, args []string) error {
			targets, err := selectTargets(args)
			if err != nil {
				return err
			}
			for _, t := range targets {
				if err := t.uninstall(); err != nil {
					return proto.Errf(proto.CodeConfig, "", "%s: %v", t.Name, err)
				}
				t.Status = t.check()
			}
			report(targets, "")
			return nil
		},
	}
	c.AddCommand(status, install, uninstall)
	return c
}

// agentsHint suggests `ship agents install` if no detected agent knows about ship yet.
func agentsHint() string {
	var found []string
	for _, t := range agentTargets() {
		if t.Status == "installed" || t.Status == "outdated" {
			return ""
		}
		if t.Found {
			found = append(found, t.Name)
		}
	}
	if len(found) == 0 {
		return ""
	}
	return fmt.Sprintf("  tip: run `ship agents install` so your coding agents (%s) deploy with ship", strings.Join(found, ", "))
}
