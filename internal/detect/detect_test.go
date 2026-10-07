package detect

import (
	"os"
	"path/filepath"
	"testing"
)

func project(t *testing.T, files map[string]string) string {
	dir := t.TempDir()
	for name, body := range files {
		p := filepath.Join(dir, name)
		os.MkdirAll(filepath.Dir(p), 0o755)
		if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}

func TestDetect(t *testing.T) {
	cases := []struct {
		name    string
		files   map[string]string
		stack   string
		command string
		install string
	}{
		{"vite pnpm", map[string]string{"package.json": `{"scripts":{"dev":"vite"}}`, "pnpm-lock.yaml": "", "node_modules/.keep": ""},
			"Vite", "pnpm dev --port $PORT --strictPort", ""},
		{"next npm", map[string]string{"package.json": `{"scripts":{"dev":"next dev"}}`, "node_modules/.keep": ""},
			"Next.js", "npm run dev -- -p $PORT", ""},
		{"sveltekit", map[string]string{"package.json": `{"scripts":{"dev":"vite dev"}}`, "yarn.lock": "", "node_modules/.keep": ""},
			"Vite", "yarn dev --port $PORT --strictPort", ""},
		{"script sets port", map[string]string{"package.json": `{"scripts":{"dev":"vite --port 3000"}}`, "node_modules/.keep": ""},
			"Vite", "npm run dev", ""},
		{"node without deps", map[string]string{"package.json": `{"scripts":{"start":"node index.js"}}`, "bun.lock": ""},
			"Node.js", "bun run start", "bun install"},
		{"cra", map[string]string{"package.json": `{"scripts":{"start":"react-scripts start"}}`, "node_modules/.keep": ""},
			"Create React App", "BROWSER=none npm run start", ""},
		{"django venv", map[string]string{"manage.py": "", ".venv/bin/python": ""},
			"Django", ".venv/bin/python manage.py runserver 127.0.0.1:$PORT", ""},
		{"fastapi uv", map[string]string{"pyproject.toml": "fastapi", "uv.lock": "", "app/main.py": "api = FastAPI()\n"},
			"FastAPI", "uv run uvicorn app.main:api --reload --port $PORT", ""},
		{"flask", map[string]string{"requirements.txt": "flask", "app.py": "app = Flask(__name__)\n"},
			"Flask", "flask --app app:app run --debug --port $PORT", ""},
		{"go cmd", map[string]string{"go.mod": "module x", "cmd/api/main.go": "package main\n"},
			"Go", "go run ./cmd/api", ""},
		{"static", map[string]string{"index.html": "<h1>hi</h1>"},
			"static site", "python3 -m http.server $PORT --bind 127.0.0.1", ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			r := Detect(project(t, c.files))
			if r == nil {
				t.Fatal("nothing detected")
			}
			if r.Stack != c.stack || r.Command != c.command || r.Install != c.install {
				t.Errorf("got %q / %q / install %q, want %q / %q / %q", r.Stack, r.Command, r.Install, c.stack, c.command, c.install)
			}
		})
	}
	if r := Detect(project(t, map[string]string{"notes.txt": ""})); r != nil {
		t.Errorf("detected %+v in a folder with no project", r)
	}
}
