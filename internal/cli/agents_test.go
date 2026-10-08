package cli

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestAgentsInstallUninstall(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", "")
	os.MkdirAll(filepath.Join(home, ".claude", "skills", "ship"), 0o755)
	os.MkdirAll(filepath.Join(home, ".codex"), 0o755)
	codex := filepath.Join(home, ".codex", "AGENTS.md")
	// ship's notes, which install replaces
	os.WriteFile(codex, []byte("# Mine\n\nkeep me\n\n<!-- ship:start -->\nold\n<!-- ship:end -->\n"), 0o644)
	shipSkill := filepath.Join(home, ".claude", "skills", "ship")
	os.WriteFile(filepath.Join(shipSkill, "SKILL.md"), []byte("<!-- managed by `ship agents` -->"), 0o644)

	for _, tg := range agentTargets() {
		if tg.Found {
			if err := tg.install(); err != nil {
				t.Fatal(err)
			}
			if err := tg.install(); err != nil { // idempotent
				t.Fatal(err)
			}
		}
	}
	b, _ := os.ReadFile(codex)
	if !strings.HasPrefix(string(b), "# Mine\n\nkeep me\n\n") || strings.Count(string(b), blockStart) != 1 || strings.Contains(string(b), "ship:start") {
		t.Fatalf("codex file:\n%s", b)
	}
	if _, err := os.Stat(shipSkill); !os.IsNotExist(err) {
		t.Fatal("ship's skill should be removed")
	}
	for _, tg := range agentTargets() {
		want := map[bool]string{true: "installed", false: "not_detected"}[tg.Found]
		if tg.Status != want {
			t.Errorf("%s: status %s, want %s", tg.Name, tg.Status, want)
		}
	}

	for _, tg := range agentTargets() {
		if err := tg.uninstall(); err != nil {
			t.Fatal(err)
		}
	}
	if b, _ := os.ReadFile(codex); string(b) != "# Mine\n\nkeep me\n" {
		t.Fatalf("codex file after uninstall:\n%q", b)
	}
	if _, err := os.Stat(filepath.Join(home, ".claude", "skills", "homebase")); !os.IsNotExist(err) {
		t.Fatal("skill dir should be removed")
	}
}
