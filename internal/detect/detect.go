// Package detect works out how to run a project: Detect finds the command
// that starts its dev server on $PORT, Build how to build its production image.
package detect

import (
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

type Result struct {
	Stack   string `json:"stack"`   // human name: "Vite", "Django", ...
	Command string `json:"command"` // shell command; $PORT is expanded at run time
	// Note explains an assumption the command relies on.
	Note string `json:"note,omitempty"`
	// Install is the command that installs dependencies, if they look
	// missing (e.g. no node_modules).
	Install string `json:"install,omitempty"`
}

// Detect returns nil if nothing it knows is found in dir.
func Detect(dir string) *Result {
	for _, f := range []func(string) *Result{node, python, rails, golang, rust, deno, static} {
		if r := f(dir); r != nil {
			return r
		}
	}
	return nil
}

// LooksLikeProject reports whether dir is a code project at all, to tell
// "unknown stack" apart from "not a project".
func LooksLikeProject(dir string) bool {
	for _, f := range []string{".git", "package.json", "pyproject.toml", "requirements.txt", "go.mod",
		"Cargo.toml", "Gemfile", "deno.json", "index.html", "Makefile", "Dockerfile", "Procfile"} {
		if exists(filepath.Join(dir, f)) {
			return true
		}
	}
	return false
}

func exists(p string) bool {
	_, err := os.Stat(p)
	return err == nil
}

func read(p string) string {
	b, _ := os.ReadFile(p)
	return string(b)
}

// ---- Node -----------------------------------------------------------------

// nodeFrameworks maps a dev script to the framework and the flags that set
// its port. Order matters: "remix vite:dev" must match before "vite".
var nodeFrameworks = []struct {
	match, name, args string
}{
	{"next", "Next.js", "-p $PORT"},
	{"nuxi", "Nuxt", "--port $PORT"},
	{"nuxt", "Nuxt", "--port $PORT"},
	{"astro", "Astro", "--port $PORT"},
	{"react-router dev", "React Router", "--port $PORT"},
	{"remix vite:dev", "Remix", "--port $PORT"},
	{"vite", "Vite", "--port $PORT --strictPort"},
	{"ng serve", "Angular", "--port $PORT"},
	{"webpack serve", "webpack", "--port $PORT"},
	{"webpack-dev-server", "webpack", "--port $PORT"},
	{"gatsby develop", "Gatsby", "-p $PORT"},
}

func node(dir string) *Result {
	raw := read(filepath.Join(dir, "package.json"))
	if raw == "" {
		return nil
	}
	var pkg struct {
		Scripts map[string]string `json:"scripts"`
	}
	json.Unmarshal([]byte(raw), &pkg)

	pm := packageManager(dir)
	r := &Result{Stack: "Node.js"}
	if !nodeModulesFound(dir) {
		r.Install = pm + " install"
	}

	script := ""
	for _, s := range []string{"dev", "start", "serve"} {
		if pkg.Scripts[s] != "" {
			script = s
			break
		}
	}
	if script == "" {
		if exists(filepath.Join(dir, "server.js")) {
			r.Command, r.Note = "node server.js", "assumes server.js listens on $PORT"
			return r
		}
		return nil
	}

	run := map[string]string{"npm": "npm run ", "pnpm": "pnpm ", "yarn": "yarn ", "bun": "bun run "}[pm] + script
	body := pkg.Scripts[script]
	var args string
	for _, f := range nodeFrameworks {
		if regexp.MustCompile(`(^|[\s/&;])` + regexp.QuoteMeta(f.match) + `($|[\s:])`).MatchString(body) {
			r.Stack, args = f.name, f.args
			break
		}
	}
	switch {
	case strings.Contains(body, "react-scripts start"):
		r.Stack = "Create React App"
		run = "BROWSER=none " + run // CRA reads $PORT; don't open a browser tab
	case args == "":
		r.Note = "assumes the app reads $PORT"
	case strings.Contains(body, "--port") || regexp.MustCompile(`\s-p\s`).MatchString(body):
		args = "" // the script sets a port itself
		r.Note = "the script sets its own port; homebase expects it on $PORT"
	}
	if args != "" {
		if pm == "npm" {
			run += " --"
		}
		run += " " + args
	}
	r.Command = run
	return r
}

// packageManager picks npm, pnpm, yarn or bun from the lockfile.
func packageManager(dir string) string {
	switch {
	case exists(filepath.Join(dir, "pnpm-lock.yaml")):
		return "pnpm"
	case exists(filepath.Join(dir, "yarn.lock")):
		return "yarn"
	case exists(filepath.Join(dir, "bun.lock")), exists(filepath.Join(dir, "bun.lockb")):
		return "bun"
	}
	return "npm"
}

// nodeModulesFound looks in dir and its parents (workspaces hoist
// node_modules to the repo root); Yarn Plug'n'Play has none at all.
func nodeModulesFound(dir string) bool {
	for d := dir; ; d = filepath.Dir(d) {
		if exists(filepath.Join(d, "node_modules")) || exists(filepath.Join(d, ".pnp.cjs")) {
			return true
		}
		if filepath.Dir(d) == d {
			return false
		}
	}
}

// ---- Python ---------------------------------------------------------------

// pyPrefix returns how to run a Python tool in this project: through uv, a
// local virtualenv, or the system.
func pyPrefix(dir string) (python, bin string) {
	switch {
	case exists(filepath.Join(dir, "uv.lock")):
		return "uv run python", "uv run "
	case exists(filepath.Join(dir, ".venv", "bin", "python")):
		return ".venv/bin/python", ".venv/bin/"
	case exists(filepath.Join(dir, "venv", "bin", "python")):
		return "venv/bin/python", "venv/bin/"
	}
	return "python3", ""
}

var appVarRe = regexp.MustCompile(`(?m)^(\w+)\s*=\s*(FastAPI|Flask)\(`)

func python(dir string) *Result {
	deps := strings.ToLower(read(filepath.Join(dir, "requirements.txt")) + read(filepath.Join(dir, "pyproject.toml")))
	py, bin := pyPrefix(dir)
	if exists(filepath.Join(dir, "manage.py")) {
		return &Result{Stack: "Django", Command: py + " manage.py runserver 127.0.0.1:$PORT"}
	}
	if deps == "" && !exists(filepath.Join(dir, "main.py")) && !exists(filepath.Join(dir, "app.py")) {
		return nil
	}
	for _, f := range []string{"main.py", "app.py", "app/main.py", "src/main.py", "api/main.py", "wsgi.py"} {
		m := appVarRe.FindStringSubmatch(read(filepath.Join(dir, f)))
		if m == nil {
			continue
		}
		module := strings.ReplaceAll(strings.TrimSuffix(f, ".py"), "/", ".")
		if m[2] == "FastAPI" {
			return &Result{Stack: "FastAPI", Command: bin + "uvicorn " + module + ":" + m[1] + " --reload --port $PORT"}
		}
		return &Result{Stack: "Flask", Command: bin + "flask --app " + module + ":" + m[1] + " run --debug --port $PORT"}
	}
	return nil
}

// ---- others ---------------------------------------------------------------

func rails(dir string) *Result {
	if !strings.Contains(read(filepath.Join(dir, "Gemfile")), "rails") || !exists(filepath.Join(dir, "bin", "rails")) {
		return nil
	}
	return &Result{Stack: "Rails", Command: "bin/rails server -p $PORT"}
}

func golang(dir string) *Result {
	if !exists(filepath.Join(dir, "go.mod")) {
		return nil
	}
	r := &Result{Stack: "Go", Note: "assumes the app reads $PORT"}
	if hasMainPackage(dir) {
		r.Command = "go run ."
		return r
	}
	mains, _ := filepath.Glob(filepath.Join(dir, "cmd", "*"))
	var found []string
	for _, m := range mains {
		if hasMainPackage(m) {
			found = append(found, m)
		}
	}
	if len(found) != 1 {
		return nil // no main package, or several to choose from
	}
	r.Command = "go run ./cmd/" + filepath.Base(found[0])
	return r
}

func hasMainPackage(dir string) bool {
	files, _ := filepath.Glob(filepath.Join(dir, "*.go"))
	for _, f := range files {
		if regexp.MustCompile(`(?m)^package main\b`).MatchString(read(f)) {
			return true
		}
	}
	return false
}

func rust(dir string) *Result {
	if !exists(filepath.Join(dir, "Cargo.toml")) {
		return nil
	}
	return &Result{Stack: "Rust", Command: "cargo run", Note: "assumes the app reads $PORT"}
}

func deno(dir string) *Result {
	cfg := read(filepath.Join(dir, "deno.json"))
	if cfg == "" {
		return nil
	}
	var d struct {
		Tasks map[string]string `json:"tasks"`
	}
	json.Unmarshal([]byte(cfg), &d)
	if d.Tasks["dev"] == "" {
		return nil
	}
	return &Result{Stack: "Deno", Command: "deno task dev", Note: "assumes the app reads $PORT"}
}

func static(dir string) *Result {
	if !exists(filepath.Join(dir, "index.html")) {
		return nil
	}
	return &Result{Stack: "static site", Command: "python3 -m http.server $PORT --bind 127.0.0.1"}
}
