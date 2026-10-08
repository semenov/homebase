package cli

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/semenov/homebase/internal/detect"
)

func TestShellQuote(t *testing.T) {
	for in, want := range map[string]string{
		"deploy":          "deploy",
		"ship/app:1":      "ship/app:1",
		"":                "''",
		"K=a b":           "'K=a b'",
		"it's":            `'it'"'"'s'`,
		"$(rm -rf /)":     "'$(rm -rf /)'",
		"GREETING=привет": "'GREETING=привет'",
	} {
		if got := shellQuote(in); got != want {
			t.Errorf("shellQuote(%q) = %s, want %s", in, got, want)
		}
	}
}

func TestNameFromDir(t *testing.T) {
	for in, want := range map[string]string{
		"/x/My App":   "my-app",
		"/x/api_v2":   "api-v2",
		"/x/hello.io": "hello-io",
	} {
		if got, err := nameFromDir(in); err != nil || got != want {
			t.Errorf("nameFromDir(%q) = %q, %v; want %q", in, got, err, want)
		}
	}
	if _, err := nameFromDir("/x/Проект"); err == nil {
		t.Error("a name without latin letters or digits should fail")
	}
}

func TestEject(t *testing.T) {
	t.Setenv("HOMEBASE_CONFIG", filepath.Join(t.TempDir(), "servers.yaml"))
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
	plan, _ := detect.Build(dir, "", "")
	if plan.Stack != "dockerfile" || plan.Port != 80 {
		t.Errorf("after eject: %s:%d", plan.Stack, plan.Port)
	}
	if err := c.Execute(); err == nil {
		t.Error("second eject should refuse (nothing to eject)")
	}
}
