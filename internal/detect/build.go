package detect

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"

	"github.com/semenov/homebase/internal/proto"
)

// Plan is how to build the production image of a project.
type Plan struct {
	Stack string // dockerfile, node, node-static, python, go, static
	// Dockerfile is the path (relative to the project dir) of a user Dockerfile.
	// Empty when Generated is set.
	Dockerfile string
	Generated  string
	Port       int
	// Ignore holds .dockerignore rules used when the project has no .dockerignore.
	Ignore string
}

// DefaultIgnore is used for generated Dockerfiles when the project has no .dockerignore.
const DefaultIgnore = ".git\nnode_modules\n.venv\nvenv\n__pycache__\n*.pyc\ntarget\n.env\n.env.*\n.DS_Store\nhomebase.toml\nship.toml\n"

// StaticIgnore is stricter: everything in the image of a plain static site is
// published, so hidden files and folders (agent and editor settings, .env, .git)
// and project files that are not part of the site stay out.
const StaticIgnore = ".*\n**/.*\n!.well-known\n!.well-known/**\nnode_modules\n*.md\nhomebase.toml\nship.toml\nDockerfile*\n"

// staticNginx configures nginx for static sites: gzip, and hidden files are
// never served even if they end up in the image. spa serves index.html for
// unknown paths (client-side routing).
func staticNginx(spa bool) string {
	fallback := "=404"
	if spa {
		fallback = "/index.html"
	}
	conf := `server {
  listen 80;
  root /usr/share/nginx/html;
  index index.html;
  gzip on;
  gzip_proxied any;
  gzip_types text/plain text/css application/javascript application/json image/svg+xml;
  location ~ /\.(?!well-known/) { return 404; }
  location / { try_files $uri $uri/ ` + fallback + `; }
}
`
	// printf expands the \n escapes; the config contains no % or ' characters
	return "RUN printf '" + strings.ReplaceAll(conf, "\n", "\\n") + "' > /etc/nginx/conf.d/default.conf\n"
}

var exposeRe = regexp.MustCompile(`(?mi)^\s*EXPOSE\s+(\d+)`)

// Build works out how to build a production image of dir. dockerfile overrides
// the Dockerfile path, start overrides the start command.
func Build(dir, dockerfile, start string) (*Plan, error) {
	if dockerfile == "" && exists(filepath.Join(dir, "Dockerfile")) {
		dockerfile = "Dockerfile"
	}
	if dockerfile != "" {
		b, err := os.ReadFile(filepath.Join(dir, dockerfile))
		if err != nil {
			return nil, proto.Errf(proto.CodeConfig, "", "cannot read %s: %v", dockerfile, err)
		}
		p := &Plan{Stack: "dockerfile", Dockerfile: dockerfile, Port: 8080, Ignore: DefaultIgnore}
		if m := exposeRe.FindSubmatch(b); m != nil {
			p.Port, _ = strconv.Atoi(string(m[1]))
		}
		return p, nil
	}
	p, err := buildStack(dir, start)
	if err == nil && p.Ignore == "" {
		p.Ignore = DefaultIgnore
	}
	return p, err
}

func buildStack(dir, start string) (*Plan, error) {
	switch {
	case exists(filepath.Join(dir, "package.json")):
		return buildNode(dir, start)
	case exists(filepath.Join(dir, "requirements.txt")) || exists(filepath.Join(dir, "pyproject.toml")):
		return buildPython(dir, start)
	case exists(filepath.Join(dir, "go.mod")):
		return buildGo(dir, start)
	case exists(filepath.Join(dir, "Cargo.toml")):
		return buildRust(dir, start)
	case exists(filepath.Join(dir, "index.html")):
		return &Plan{Stack: "static", Port: 80, Ignore: StaticIgnore,
			Generated: "FROM nginx:alpine\n" + staticNginx(false) + "COPY . /usr/share/nginx/html\nEXPOSE 80\n"}, nil
	}
	return nil, proto.Errf(proto.CodeStackUnknown,
		"add a Dockerfile, or make sure the project root has package.json, requirements.txt/pyproject.toml, go.mod, Cargo.toml or index.html",
		"could not detect how to build %s", dir)
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

func buildNode(dir, start string) (*Plan, error) {
	var pkg packageJSON
	if err := json.Unmarshal([]byte(read(filepath.Join(dir, "package.json"))), &pkg); err != nil {
		return nil, proto.Errf(proto.CodeConfig, "", "invalid package.json: %v", err)
	}
	pm, install := packageManager(dir), ""
	switch pm {
	case "pnpm":
		install = "corepack enable && pnpm install --frozen-lockfile"
	case "yarn":
		install = "corepack enable && yarn install"
	default: // the node image has no bun
		pm, install = "npm", "npm install"
		if exists(filepath.Join(dir, "package-lock.json")) {
			install = "npm ci"
		}
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
		} else if pkg.Main != "" && exists(filepath.Join(dir, pkg.Main)) {
			start = "node " + pkg.Main
		} else {
			for _, f := range []string{"server.js", "index.js", "app.js", "main.js", "server.mjs", "index.mjs"} {
				if exists(filepath.Join(dir, f)) {
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
		return &Plan{Stack: "node-static", Port: 80, Generated: head + "\nFROM nginx:alpine\n" + staticNginx(true) +
			"COPY --from=build /app/" + out + " /usr/share/nginx/html\nEXPOSE 80\n"}, nil
	}
	if start == "" {
		return nil, proto.Errf(proto.CodeStackUnknown, `add a "start" script to package.json or set start = "..." under [deploy] in homebase.toml`, "cannot find how to start the node app")
	}
	return &Plan{Stack: "node", Port: 3000, Generated: head + "ENV NODE_ENV=production PORT=3000\nEXPOSE 3000\nCMD " + shellCMD(start) + "\n"}, nil
}

var (
	fastapiRe = regexp.MustCompile(`(\w+)\s*=\s*FastAPI\(`)
	flaskRe   = regexp.MustCompile(`(\w+)\s*=\s*Flask\(`)
)

func buildPython(dir, start string) (*Plan, error) {
	extra := ""
	if start == "" {
		if exists(filepath.Join(dir, "manage.py")) {
			if matches, _ := filepath.Glob(filepath.Join(dir, "*", "wsgi.py")); len(matches) > 0 {
				proj := filepath.Base(filepath.Dir(matches[0]))
				start, extra = "gunicorn "+proj+".wsgi --bind 0.0.0.0:$PORT", "gunicorn"
			}
		}
	}
	if start == "" {
		for _, f := range []string{"main.py", "app.py", "server.py", "api.py"} {
			src := read(filepath.Join(dir, f))
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
		return nil, proto.Errf(proto.CodeStackUnknown, `set start = "..." under [deploy] in homebase.toml (it must listen on 0.0.0.0:$PORT)`, "cannot find how to start the python app")
	}
	var b strings.Builder
	b.WriteString("FROM python:3.12-slim\nWORKDIR /app\nENV PYTHONUNBUFFERED=1 PORT=8000\n")
	if exists(filepath.Join(dir, "requirements.txt")) {
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

func buildGo(dir, start string) (*Plan, error) {
	image := "golang:alpine"
	if m := goVersionRe.FindStringSubmatch(read(filepath.Join(dir, "go.mod"))); m != nil {
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
