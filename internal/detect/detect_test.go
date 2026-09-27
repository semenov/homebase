package detect

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func project(t *testing.T, files map[string]string) string {
	dir := t.TempDir()
	for name, content := range files {
		path := filepath.Join(dir, name)
		os.MkdirAll(filepath.Dir(path), 0o755)
		if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}

func TestDetect(t *testing.T) {
	tests := []struct {
		name     string
		files    map[string]string
		stack    string
		port     int
		contains []string
	}{
		{"dockerfile expose", map[string]string{"Dockerfile": "FROM x\nEXPOSE 4000\n", "package.json": "{}"}, "dockerfile", 4000, nil},
		{"dockerfile no expose", map[string]string{"Dockerfile": "FROM x\n"}, "dockerfile", 8080, nil},
		{"node start script", map[string]string{"package.json": `{"scripts":{"start":"node s.js","build":"tsc"}}`, "package-lock.json": "{}"},
			"node", 3000, []string{"npm ci", "npm run build", `exec npm start`}},
		{"node pnpm", map[string]string{"package.json": `{"scripts":{"start":"x"}}`, "pnpm-lock.yaml": ""}, "node", 3000, []string{"pnpm install --frozen-lockfile", "exec pnpm start"}},
		{"node server.js", map[string]string{"package.json": `{}`, "server.js": ""}, "node", 3000, []string{"exec node server.js"}},
		{"vite spa", map[string]string{"package.json": `{"scripts":{"build":"vite build"},"devDependencies":{"vite":"5"}}`}, "node-static", 80, []string{"/app/dist", "try_files"}},
		{"cra spa", map[string]string{"package.json": `{"scripts":{"build":"react-scripts build"},"dependencies":{"react-scripts":"5"}}`}, "node-static", 80, []string{"/app/build"}},
		{"fastapi", map[string]string{"requirements.txt": "fastapi", "main.py": "from fastapi import FastAPI\napi = FastAPI()\n"}, "python", 8000, []string{"uvicorn main:api", "pip install --no-cache-dir uvicorn"}},
		{"flask", map[string]string{"requirements.txt": "flask", "app.py": "app = Flask(__name__)"}, "python", 8000, []string{"gunicorn app:app --bind 0.0.0.0:$PORT"}},
		{"django", map[string]string{"requirements.txt": "django", "manage.py": "", "mysite/wsgi.py": ""}, "python", 8000, []string{"gunicorn mysite.wsgi"}},
		{"go root", map[string]string{"go.mod": "module x\n\ngo 1.23.1\n", "main.go": "package main"}, "go", 8080, []string{"golang:1.23-alpine", "-o /out/app .\n"}},
		{"go cmd", map[string]string{"go.mod": "module x\n", "cmd/server/main.go": "package main"}, "go", 8080, []string{"./cmd/server"}},
		{"static", map[string]string{"index.html": "<h1>"}, "static", 80, []string{"nginx:alpine"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			p, err := Detect(project(t, tt.files), "", "")
			if err != nil {
				t.Fatal(err)
			}
			if p.Stack != tt.stack || p.Port != tt.port {
				t.Fatalf("got %s:%d, want %s:%d", p.Stack, p.Port, tt.stack, tt.port)
			}
			for _, c := range tt.contains {
				if !strings.Contains(p.Generated, c) {
					t.Errorf("generated Dockerfile lacks %q:\n%s", c, p.Generated)
				}
			}
		})
	}
}

func TestDetectFailures(t *testing.T) {
	for name, files := range map[string]map[string]string{
		"empty":         {"README.md": ""},
		"node no start": {"package.json": `{}`},
		"python no app": {"requirements.txt": ""},
	} {
		if _, err := Detect(project(t, files), "", ""); err == nil {
			t.Errorf("%s: expected error", name)
		}
	}
}

func TestStartOverride(t *testing.T) {
	p, err := Detect(project(t, map[string]string{"requirements.txt": ""}), "", "python worker.py")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(p.Generated, `"exec python worker.py"`) {
		t.Fatal(p.Generated)
	}
}

func TestRust(t *testing.T) {
	p, err := Detect(project(t, map[string]string{
		"Cargo.toml": "[package]\nname = \"api\"\nversion = \"0.1.0\"\n", "src/main.rs": "fn main() {}",
	}), "", "")
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"rust:1-bookworm", "cargo build --release && rm -rf src", "--bin api", "target/release/api /usr/local/bin/app", "libssl3"} {
		if !strings.Contains(p.Generated, want) {
			t.Errorf("missing %q:\n%s", want, p.Generated)
		}
	}
	if p.Stack != "rust" || p.Port != 8080 {
		t.Errorf("got %s:%d", p.Stack, p.Port)
	}

	// custom bin path and build.rs: no dependency-caching stubs
	p, err = Detect(project(t, map[string]string{
		"Cargo.toml": "[package]\nname = \"x\"\n\n[[bin]]\nname = \"server\"\npath = \"bin/server.rs\"\n", "bin/server.rs": "", "build.rs": "",
	}), "", "")
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(p.Generated, "rm -rf src") || !strings.Contains(p.Generated, "--bin server") {
		t.Errorf("unexpected:\n%s", p.Generated)
	}

	for name, files := range map[string]map[string]string{
		"workspace": {"Cargo.toml": "[workspace]\nmembers = [\"a\"]\n"},
		"lib only":  {"Cargo.toml": "[package]\nname = \"x\"\n", "src/lib.rs": ""},
		"many bins": {"Cargo.toml": "[package]\nname = \"x\"\n[[bin]]\nname = \"a\"\n[[bin]]\nname = \"b\"\n"},
	} {
		if _, err := Detect(project(t, files), "", ""); err == nil {
			t.Errorf("%s: expected error", name)
		}
	}
}
