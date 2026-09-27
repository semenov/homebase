package cli

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/semenov/ship/internal/detect"
)

func writeTree(t *testing.T, files map[string]string) string {
	dir := t.TempDir()
	for name, content := range files {
		p := filepath.Join(dir, name)
		os.MkdirAll(filepath.Dir(p), 0o755)
		os.WriteFile(p, []byte(content), 0o644)
	}
	return dir
}

func TestStaticContext(t *testing.T) {
	dir := writeTree(t, map[string]string{
		"index.html": "<h1>", "img/a.png": "png", "DEPLOY.md": "x",
		".claude/settings.local.json": "{}", ".playwright-mcp/log.yml": "x", ".well-known/security.txt": "x",
		"ship.toml": "", "sub/.hidden": "x",
	})
	plan, err := detect.Detect(dir, "", "")
	if err != nil {
		t.Fatal(err)
	}
	ci, err := inspectContext(dir, plan, false)
	if err != nil {
		t.Fatal(err)
	}
	if ci.Files != 3 { // index.html, img/a.png, .well-known/security.txt
		t.Errorf("files = %d, want 3 (%s)", ci.Files, ci)
	}
	got := strings.Join(ci.Excluded, " ")
	for _, want := range []string{".claude/", ".playwright-mcp/", "DEPLOY.md", "ship.toml", "sub/.hidden"} {
		if !strings.Contains(got, want) {
			t.Errorf("excluded %q lacks %s", got, want)
		}
	}
	if strings.Contains(got, "security.txt") || len(ci.Warnings) != 0 {
		t.Errorf("unexpected: excluded=%q warnings=%v", got, ci.Warnings)
	}
	if !strings.Contains(plan.Generated, `location ~ /\.(?!well-known/) { return 404; }`) {
		t.Errorf("nginx must refuse hidden files:\n%s", plan.Generated)
	}
}

func TestDockerfileContextWarnsAboutSecrets(t *testing.T) {
	dir := writeTree(t, map[string]string{"Dockerfile": "FROM x\n", ".env": "SECRET=1", "main.go": ""})
	plan, _ := detect.Detect(dir, "", "")
	ci, err := inspectContext(dir, plan, false)
	if err != nil {
		t.Fatal(err)
	}
	if len(ci.Warnings) != 1 || !strings.Contains(ci.Warnings[0], ".env") {
		t.Errorf("expected a warning about .env, got %v", ci.Warnings)
	}
	os.WriteFile(filepath.Join(dir, ".dockerignore"), []byte(".env\n"), 0o644)
	if ci, _ = inspectContext(dir, plan, false); len(ci.Warnings) != 0 || ci.Excluded[0] != ".env" {
		t.Errorf("with .dockerignore: %+v", ci)
	}
}

func TestEject(t *testing.T) {
	dir := writeTree(t, map[string]string{"index.html": "<h1>"})
	jsonOut = true
	defer func() { jsonOut = false }()
	c := ejectCmd()
	c.SetArgs([]string{dir})
	c.SetOut(os.Stderr)
	if err := c.Execute(); err != nil {
		t.Fatal(err)
	}
	df, _ := os.ReadFile(filepath.Join(dir, "Dockerfile"))
	ig, _ := os.ReadFile(filepath.Join(dir, ".dockerignore"))
	if !strings.Contains(string(df), "nginx:alpine") || string(ig) != detect.StaticIgnore {
		t.Fatalf("Dockerfile:\n%s\n.dockerignore:\n%s", df, ig)
	}
	plan, _ := detect.Detect(dir, "", "")
	if plan.Stack != "dockerfile" || plan.Port != 80 {
		t.Errorf("after eject: %s:%d", plan.Stack, plan.Port)
	}
	if err := c.Execute(); err == nil {
		t.Error("second eject should refuse (nothing to eject)")
	}
}
