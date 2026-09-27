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
	os.MkdirAll(filepath.Join(home, ".claude"), 0o755)
	os.MkdirAll(filepath.Join(home, ".codex"), 0o755)
	codex := filepath.Join(home, ".codex", "AGENTS.md")
	os.WriteFile(codex, []byte("# Mine\n\nkeep me\n"), 0o644)

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
	if !strings.HasPrefix(string(b), "# Mine\n\nkeep me\n\n") || strings.Count(string(b), blockStart) != 1 {
		t.Fatalf("codex file:\n%s", b)
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
	if _, err := os.Stat(filepath.Join(home, ".claude", "skills", "ship")); !os.IsNotExist(err) {
		t.Fatal("skill dir should be removed")
	}
}
