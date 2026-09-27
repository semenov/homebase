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
      --volume /data                keep this container path across deploys (repeatable)
      --memory 256m                 memory limit; the app is restarted if it exceeds it
      --release "npm run migrate"   run in the new image before traffic switches
      --remote-build                build on the server instead of locally
      --timeout 90s                 health check timeout
    ship status                     URL, state, health, CPU/memory/disk usage, releases
    ship ls                         all apps with CPU, memory, disk, plus server memory/disk/load
    ship logs [-n 100] [-f] [--since 10m]
    ship rollback                   back to the previous release (zero downtime)
    ship restart                    restart current release (zero downtime)
    ship env ls [--reveal]          env vars (stored on the server, 0600)
    ship env set K=V [K2=V2...]     set and restart (use --no-restart to skip)
    ship env unset K...
    ship db add                     create a Postgres database for the app, sets DATABASE_URL
    ship db info                    database and its backups
    ship db shell [-c "SQL"]        psql (interactive, or one statement with -c)
    ship db backup [--download|-o F] dump now (nightly backups run automatically, last 7 kept)
    ship db restore FILE --yes      replace the database (pg_dump -Fc or .sql); safety backup first
    ship destroy --yes              remove containers, images, route; KEEPS volumes, database, env
    ship destroy --data --yes       also delete volumes and the database (final backup is kept)
    ship init user@host [--install] [--base-domain apps.example.com] [--no-default]
    ship agents install|uninstall|status   tell coding agents on this machine to deploy with ship

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

## Data: files and databases

The container is replaced on every deploy, rollback and restart, so anything written inside it
is lost. Persist data in one of two ways:

- Files (uploads, SQLite): `volumes = ["/data"]` in ship.toml. Each path is a docker volume
  (`ship-<app>-data`) mounted into every release; the app gets `DATA_DIR=/data`. For SQLite,
  put the database file there and enable WAL mode.
- Postgres: `ship db add` (can run before the first deploy). The server runs one shared
  Postgres container (`ship-postgres`, not exposed to the internet); each app gets its own
  database and role, and `DATABASE_URL` in its env. Nightly `pg_dump` backups go to
  `/var/lib/ship/backups/<app>/` (last 7 kept).

Schema migrations: `release = "npm run migrate"` in ship.toml. It runs once in the new image,
with the app's env and volumes, before traffic switches; if it fails (`release_command_failed`),
the old release keeps serving. It does not run on rollback or restart, so keep migrations
backward compatible with the previous release.

Volumes are only ever added by deploys; removing a path from ship.toml does not delete it.

## ship.toml

    name = "my-app"
    server = "root@1.2.3.4"
    domain = "my-app.example.com"   # optional
    port = 3000                     # optional, container port
    health = "/healthz"             # optional
    start = "node dist/server.js"   # optional, only without Dockerfile
    dockerfile = "deploy/Dockerfile" # optional
    build = "remote"                # optional: build on the server
    volumes = ["/data"]             # optional: persistent paths
    release = "npm run migrate"     # optional: runs before traffic switches
    memory = "256m"                 # optional: memory limit (removing it removes the limit)

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
  `proxy_config_failed`(5) `deploy_in_progress`(5) `release_command_failed`(5)
  `database_failed`(5) `ssh_failed`(6) `app_not_found`(7)
  `no_previous_release`(7) `internal`(1).
- On `container_failed`/`health_check_failed`, `error.logs` holds the last container output;
  the previous release is still serving. Fix and redeploy.
- To verify a deploy: `ship status --json` => `data.healthy == true`.
- Resource usage: `data.resources` (`cpu_percent`, `mem_bytes`, `mem_limit_bytes`, `volume_bytes`,
  `database_bytes`) and `data.oom_kills` (> 0 means the app hit its memory limit: raise `memory`).
  `ship ls --json` adds `server` with `mem_available_bytes`, `disk_free_bytes`, `load1`, `cpus`.
- Secrets: `ship env set KEY=value` (can be run before the first deploy); never commit them.
- `ship destroy` requires `--yes` and keeps data; only use `--data` when the user explicitly
  asks to delete the app's data.
- Apps that store files or need a database: add `volumes` and/or run `ship db add` before
  deploying; read `DATA_DIR` / `DATABASE_URL` from the environment.

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
