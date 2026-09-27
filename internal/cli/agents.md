# ship: deploy web apps to your own server

ship builds a Docker image from a project directory, uploads it to a server over
SSH and releases it behind Caddy (or an existing nginx) with automatic HTTPS.
Releases are zero-downtime: the new container must answer HTTP before traffic
switches, otherwise the old one keeps serving and the deploy fails with logs.

## Quick start

    ship init root@1.2.3.4          # once per server; --install sets up docker + caddy on Ubuntu/Debian
    cd my-app && ship               # deploy; prints the URL

The first deploy writes `ship.toml` (name + server). Commit it; later deploys are just `ship`.

## Commands

    ship [dir]                      deploy (alias: ship deploy)
      --domain app.example.com      custom domain (A record must point at the server; TLS is automatic)
      --port 3000                   port the app listens on inside the container (default: detected)
      --health /healthz             path that must answer non-5xx before the switch (default /)
      --start "cmd"                 start command when there is no Dockerfile
      --remote-build                build on the server instead of locally
      --timeout 90s                 health check timeout
    ship status                     URL, state, health, current/previous release
    ship ls                         all apps on the server
    ship logs [-n 100] [-f] [--since 10m]
    ship rollback                   back to the previous release (zero downtime)
    ship restart                    restart current release (zero downtime)
    ship env ls [--reveal]          env vars (stored on the server, 0600)
    ship env set K=V [K2=V2...]     set and restart (use --no-restart to skip)
    ship env unset K...
    ship destroy --yes              remove app, containers, images, nginx config, env, cert
    ship init user@host [--install] [--base-domain apps.example.com] [--no-default]

Global flags: `--json`, `-a/--app NAME`, `-s/--server user@host`.

## Resolution rules

- app name: `--app` > `ship.toml` name > directory name (lowercased, [a-z0-9-])
- server: `--server` > `ship.toml` server > `$SHIP_SERVER` > default from `ship init`
- domain: `--domain` > `ship.toml` > previously deployed domain > `<app>.<base-domain>` > `<app>.<ip-with-dashes>.sslip.io`

## How the app is built

- `Dockerfile` in the root: used as-is; port from its first `EXPOSE` (else 8080).
- `package.json`: node:22 image; installs with npm/pnpm/yarn by lockfile, runs `build` script
  if present, starts with the `start` script (else `main`/server.js/index.js). No start script but a
  build script => static SPA served from `dist/` (or `build/` for react-scripts). Port 3000.
- `requirements.txt`/`pyproject.toml`: python:3.12. Detects Django (gunicorn), FastAPI (uvicorn),
  Flask (gunicorn), else `python main.py|app.py`. Port 8000.
- `go.mod`: builds `.` or the single `./cmd/*` package. Port 8080.
- `index.html`: served as a static site by nginx. Port 80.

The container always gets `PORT=<port>`; the app must listen on `0.0.0.0:$PORT`.
Without a `.dockerignore`, `.git`, `node_modules`, `.venv`, `.env*` are excluded.

## ship.toml

    name = "my-app"
    server = "root@1.2.3.4"
    domain = "my-app.example.com"   # optional
    port = 3000                     # optional, container port
    health = "/healthz"             # optional
    start = "node dist/server.js"   # optional, only without Dockerfile
    dockerfile = "deploy/Dockerfile" # optional
    build = "remote"                # optional: build on the server

## For AI agents

- Never interactive: ship does not prompt. Missing input => error with a `hint`.
- Use `--json` (or `SHIP_JSON=1`). stdout is exactly one JSON object:
  `{"ok":true,"data":{...}}` or `{"ok":false,"error":{"code","message","hint","logs"}}`.
  Progress goes to stderr and can be ignored.
- Deploy result: `data.url`, `data.tls`, `data.release.id`, `data.warnings[]`
  (e.g. the TLS certificate is not issued yet because DNS does not point at the server).
- Error codes and exit codes:
  `usage`(2) `confirmation_required`(2) `config`(3) `stack_not_detected`(3) `server_not_ready`(3)
  `build_failed`(4) `upload_failed`(4) `container_failed`(5) `health_check_failed`(5)
  `proxy_config_failed`(5) `deploy_in_progress`(5) `ssh_failed`(6) `app_not_found`(7)
  `no_previous_release`(7) `internal`(1).
- On `container_failed`/`health_check_failed`, `error.logs` holds the last container output;
  the previous release is still serving. Fix and redeploy.
- To verify a deploy: `ship status --json` => `data.healthy == true`.
- Secrets: `ship env set KEY=value` (can be run before the first deploy); never commit them.
- `ship destroy` requires `--yes`.

## Server side

ship installs `/usr/local/bin/shipd` and keeps state in `/var/lib/ship`. Containers are named
`ship-<app>-<release>`, bound to 127.0.0.1:20000-29999.

Reverse proxy: Caddy by default. Each app is one file, `/etc/caddy/ship/<app>.caddy`, imported
from `/etc/caddy/Caddyfile`; changes are applied with `caddy reload`, which is atomic and keeps
the old config if the new one is rejected. Caddy obtains and renews certificates itself and keeps
them across reloads and redeploys, so a redeploy never requests a new certificate.

If the server already runs nginx on :443, ship uses it instead: `ship-<app>.conf` in
sites-enabled (or conf.d), validated with `nginx -t` and reverted if invalid, certificates via
certbot. An SNI `stream` router on :443 is detected automatically.
