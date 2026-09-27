// Package detect figures out how to containerize a project directory.
package detect

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"

	"github.com/semenov/ship/internal/proto"
)

type Plan struct {
	Stack string // dockerfile, node, node-static, python, go, static
	// Dockerfile is the path (relative to the project dir) of a user Dockerfile.
	// Empty when Generated is set.
	Dockerfile string
	Generated  string
	Port       int
}

// DefaultIgnore is used for generated Dockerfiles when the project has no .dockerignore.
const DefaultIgnore = ".git\nnode_modules\n.venv\nvenv\n__pycache__\n*.pyc\ntarget\n.env\n.env.*\n.DS_Store\nship.toml\n"

var exposeRe = regexp.MustCompile(`(?mi)^\s*EXPOSE\s+(\d+)`)

// Detect inspects dir. dockerfile overrides the Dockerfile path, start overrides the start command.
func Detect(dir, dockerfile, start string) (*Plan, error) {
	if dockerfile == "" && exists(dir, "Dockerfile") {
		dockerfile = "Dockerfile"
	}
	if dockerfile != "" {
		b, err := os.ReadFile(filepath.Join(dir, dockerfile))
		if err != nil {
			return nil, proto.Errf(proto.CodeConfig, "", "cannot read %s: %v", dockerfile, err)
		}
		p := &Plan{Stack: "dockerfile", Dockerfile: dockerfile, Port: 8080}
		if m := exposeRe.FindSubmatch(b); m != nil {
			p.Port, _ = strconv.Atoi(string(m[1]))
		}
		return p, nil
	}
	switch {
	case exists(dir, "package.json"):
		return node(dir, start)
	case exists(dir, "requirements.txt") || exists(dir, "pyproject.toml"):
		return python(dir, start)
	case exists(dir, "go.mod"):
		return golang(dir, start)
	case exists(dir, "Cargo.toml"):
		return rust(dir, start)
	case exists(dir, "index.html"):
		return &Plan{Stack: "static", Port: 80, Generated: "FROM nginx:alpine\nCOPY . /usr/share/nginx/html\nEXPOSE 80\n"}, nil
	}
	return nil, proto.Errf(proto.CodeStackUnknown,
		"add a Dockerfile, or make sure the project root has package.json, requirements.txt/pyproject.toml, go.mod, Cargo.toml or index.html",
		"could not detect how to build %s", dir)
}

func exists(dir, name string) bool {
	_, err := os.Stat(filepath.Join(dir, name))
	return err == nil
}

func readFile(dir, name string) string {
	b, _ := os.ReadFile(filepath.Join(dir, name))
	return string(b)
}

type packageJSON struct {
	Main            string            `json:"main"`
	Scripts         map[string]string `json:"scripts"`
	Dependencies    map[string]string `json:"dependencies"`
	DevDependencies map[string]string `json:"devDependencies"`
}

func (p *packageJSON) has(dep string) bool {
	_, a := p.Dependencies[dep]
	_, b := p.DevDependencies[dep]
	return a || b
}

func node(dir, start string) (*Plan, error) {
	var pkg packageJSON
	if err := json.Unmarshal([]byte(readFile(dir, "package.json")), &pkg); err != nil {
		return nil, proto.Errf(proto.CodeConfig, "", "invalid package.json: %v", err)
	}
	pm, install := "npm", "npm install"
	switch {
	case exists(dir, "pnpm-lock.yaml"):
		pm, install = "pnpm", "corepack enable && pnpm install --frozen-lockfile"
	case exists(dir, "yarn.lock"):
		pm, install = "yarn", "corepack enable && yarn install"
	case exists(dir, "package-lock.json"):
		install = "npm ci"
	}
	run := func(script string) string {
		if pm == "npm" {
			return "npm run " + script
		}
		return pm + " " + script
	}
	build := ""
	if _, ok := pkg.Scripts["build"]; ok {
		build = "RUN " + run("build") + "\n"
	}
	head := "FROM node:22-alpine AS build\nWORKDIR /app\nCOPY package.json *.lock *-lock.yaml *-lock.json ./\nRUN " + install + "\nCOPY . .\n" + build

	if start == "" {
		if _, ok := pkg.Scripts["start"]; ok {
			start = pm + " start"
		} else if pkg.Main != "" && exists(dir, pkg.Main) {
			start = "node " + pkg.Main
		} else {
			for _, f := range []string{"server.js", "index.js", "app.js", "main.js", "server.mjs", "index.mjs"} {
				if exists(dir, f) {
					start = "node " + f
					break
				}
			}
		}
	}
	if start == "" && build != "" {
		// no server: treat as a static single-page app built into dist/ or build/
		out := "dist"
		if pkg.has("react-scripts") {
			out = "build"
		}
		return &Plan{Stack: "node-static", Port: 80, Generated: head + "\nFROM nginx:alpine\nCOPY --from=build /app/" + out + " /usr/share/nginx/html\n" +
			`RUN printf 'server {\n  listen 80;\n  root /usr/share/nginx/html;\n  location / { try_files $uri $uri/ /index.html; }\n}\n' > /etc/nginx/conf.d/default.conf` +
			"\nEXPOSE 80\n"}, nil
	}
	if start == "" {
		return nil, proto.Errf(proto.CodeStackUnknown, `add a "start" script to package.json or set start = "..." in ship.toml`, "cannot find how to start the node app")
	}
	return &Plan{Stack: "node", Port: 3000, Generated: head + "ENV NODE_ENV=production PORT=3000\nEXPOSE 3000\nCMD " + shellCMD(start) + "\n"}, nil
}

var (
	fastapiRe = regexp.MustCompile(`(\w+)\s*=\s*FastAPI\(`)
	flaskRe   = regexp.MustCompile(`(\w+)\s*=\s*Flask\(`)
)

func python(dir, start string) (*Plan, error) {
	extra := ""
	if start == "" {
		if exists(dir, "manage.py") {
			if matches, _ := filepath.Glob(filepath.Join(dir, "*", "wsgi.py")); len(matches) > 0 {
				proj := filepath.Base(filepath.Dir(matches[0]))
				start, extra = "gunicorn "+proj+".wsgi --bind 0.0.0.0:$PORT", "gunicorn"
			}
		}
	}
	if start == "" {
		for _, f := range []string{"main.py", "app.py", "server.py", "api.py"} {
			src := readFile(dir, f)
			if src == "" {
				continue
			}
			mod := strings.TrimSuffix(f, ".py")
			if m := fastapiRe.FindStringSubmatch(src); m != nil {
				start, extra = "uvicorn "+mod+":"+m[1]+" --host 0.0.0.0 --port $PORT", "uvicorn"
			} else if m := flaskRe.FindStringSubmatch(src); m != nil {
				start, extra = "gunicorn "+mod+":"+m[1]+" --bind 0.0.0.0:$PORT", "gunicorn"
			} else {
				start = "python " + f
			}
			break
		}
	}
	if start == "" {
		return nil, proto.Errf(proto.CodeStackUnknown, `set start = "..." in ship.toml (it must listen on 0.0.0.0:$PORT)`, "cannot find how to start the python app")
	}
	var b strings.Builder
	b.WriteString("FROM python:3.12-slim\nWORKDIR /app\nENV PYTHONUNBUFFERED=1 PORT=8000\n")
	if exists(dir, "requirements.txt") {
		b.WriteString("COPY requirements.txt .\nRUN pip install --no-cache-dir -r requirements.txt\nCOPY . .\n")
	} else {
		b.WriteString("COPY . .\nRUN pip install --no-cache-dir .\n")
	}
	if extra != "" {
		b.WriteString("RUN pip install --no-cache-dir " + extra + "\n")
	}
	b.WriteString("EXPOSE 8000\nCMD " + shellCMD(start) + "\n")
	return &Plan{Stack: "python", Port: 8000, Generated: b.String()}, nil
}

var goVersionRe = regexp.MustCompile(`(?m)^go\s+(\d+\.\d+)`)

func golang(dir, start string) (*Plan, error) {
	image := "golang:alpine"
	if m := goVersionRe.FindStringSubmatch(readFile(dir, "go.mod")); m != nil {
		image = "golang:" + m[1] + "-alpine"
	}
	pkg := "."
	if mains, _ := filepath.Glob(filepath.Join(dir, "*.go")); len(mains) == 0 {
		cmds, _ := filepath.Glob(filepath.Join(dir, "cmd", "*"))
		if len(cmds) != 1 {
			return nil, proto.Errf(proto.CodeStackUnknown, "add a Dockerfile", "cannot tell which Go package to build")
		}
		pkg = "./cmd/" + filepath.Base(cmds[0])
	}
	cmd := `["/app"]`
	if start != "" {
		cmd = shellCMD(start)
	}
	return &Plan{Stack: "go", Port: 8080, Generated: fmt.Sprintf(`FROM %s AS build
WORKDIR /src
COPY go.* ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 go build -ldflags="-s -w" -o /out/app %s

FROM alpine:3.20
RUN apk add --no-cache ca-certificates tzdata
COPY --from=build /out/app /app
ENV PORT=8080
EXPOSE 8080
CMD %s
`, image, pkg, cmd)}, nil
}

// shellCMD wraps a start command in exec-form CMD so $PORT is expanded and the
// process receives SIGTERM directly.
func shellCMD(start string) string {
	b, _ := json.Marshal([]string{"sh", "-c", "exec " + start})
	return string(b)
}
