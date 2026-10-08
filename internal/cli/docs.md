# homebase: the full guide

homebase runs your projects in two places:

- On this Mac, the dev server. Each one is a launchd agent: it runs in the background,
  survives the end of your terminal or agent session, restarts after a crash, comes back
  after a reboot (until stopped) and logs to a file. A small proxy gives it a stable URL,
  http://<name>.localhost. `homebase share` publishes it at https://<name>.dev.<domain>.
- On your server (any VPS you can ssh into), the deployed app ("prod"). `homebase deploy`
  builds a Docker image, uploads it over SSH and releases it behind Caddy (or an existing
  nginx) with HTTPS at https://<name>.<domain>. Releases have zero downtime: the new
  container must answer HTTP before traffic switches, otherwise the old one keeps serving.

Commands act on the project of the current folder (or a parent of it). From elsewhere pass
-a NAME. Commands that exist on both sides act on this Mac; --prod sends them to the server.

## Starting the dev server

In the project folder (or any subfolder of it):

    homebase [--json]

1. Works out the command, from (first match): --start, `start` in homebase.toml, or
   detection (see below).
2. Picks a port: --port, `port` in homebase.toml, the port it had before, or the first
   free one from 4000. The server gets it in $PORT and must listen on it.
3. Writes homebase.toml on the first run (and when flags change it). Commit it: anyone,
   or any agent, can then just run `homebase` in a fresh clone.
4. Starts the server and waits until the port accepts connections (--timeout, default 60s).
   On a crash or timeout it fails with the log output since the start.

Running it again is safe: if nothing changed and the server runs, it just reports it.
If homebase.toml or flags changed, it restarts with the new settings.

Flags: --start '<cmd>' (single quotes, so $PORT is expanded by the server's shell),
--port N, --env KEY=VALUE (repeatable), --no-save (don't write homebase.toml),
--timeout 2m, -a NAME (project name; default: the folder name).

Dev detection:

    package.json   dev/start script with the package manager from the lockfile;
                   port flags for Next.js, Vite (SvelteKit, Remix, React Router), Astro,
                   Nuxt, Angular, webpack, Gatsby; Create React App reads $PORT
    manage.py      Django: runserver 127.0.0.1:$PORT
    FastAPI        uvicorn <module>:<app> --reload --port $PORT
    Flask          flask --app <module> run --debug --port $PORT
    Rails          bin/rails server -p $PORT
    go.mod         go run . (or ./cmd/<name>); the app must read $PORT
    Cargo.toml     cargo run; the app must read $PORT
    deno.json      deno task dev; the app must read $PORT
    index.html     python3 -m http.server $PORT

Python commands use uv (uv.lock) or a local .venv/venv when present. Node projects
without node_modules fail with `dependencies_missing` and the install command.

## Deploying

Once per server (any Ubuntu/Debian VPS you can ssh into as root or with passwordless sudo):

    homebase server add root@1.2.3.4 --install [--domain example.com]

--install sets up Docker and Caddy. With --domain, apps get https://<name>.example.com and
shares https://<name>.dev.example.com; the DNS is one record *.example.com → the server's IP
(not proxied), set up once at any DNS provider. Without a domain they get
https://<name>.<ip-with-dashes>.sslip.io, which needs no DNS at all.

Then, in the project folder:

    homebase deploy [--json]

The first deploy writes a [deploy] table to homebase.toml (with the server). Commit it.

    --domain app.example.com      custom domain (its DNS record must point at the server)
    --port 3000                   port the app listens on in the container (default: detected)
    --health /healthz             path that must answer non-5xx before the switch (default /)
    --start "cmd"                 start command in the image when there is no Dockerfile
    --volume /data                keep this container path across deploys (repeatable)
    --memory 256m                 memory limit; the app is restarted if it exceeds it
    --release "npm run migrate"   run in the new image before traffic switches
    --remote-build                build on the server instead of locally
    --timeout 90s                 health check timeout

How the image is built:

- `Dockerfile` in the root: used as is; port from its first `EXPOSE` (else 8080).
- `package.json`: node:22 image; installs with npm/pnpm/yarn by lockfile, runs the `build`
  script if present, starts with the `start` script (else `main`/server.js/index.js). No start
  script but a build script: a static SPA served from `dist/` (or `build/` for react-scripts).
  Port 3000.
- `requirements.txt`/`pyproject.toml`: python:3.12. Django (gunicorn), FastAPI (uvicorn),
  Flask (gunicorn), else `python main.py|app.py`. Port 8000.
- `go.mod`: builds `.` or the single `./cmd/*` package. Port 8080.
- `Cargo.toml`: builds the package binary (or its only `[[bin]]`) in rust:1-bookworm and runs
  it on debian:bookworm-slim. Workspaces need a Dockerfile. Port 8080.
- `index.html`: served as a static site by nginx. Port 80. Everything in the image is public,
  so hidden files (`.env`, `.git`, `.claude`, ...), `*.md`, homebase.toml and `Dockerfile` are
  left out, and nginx refuses `/.anything` except `/.well-known`.

The container always gets `PORT=<port>`; the app must listen on `0.0.0.0:$PORT`. Without a
`.dockerignore`, `.git`, `node_modules`, `.venv`, `target`, `.env*` are left out. Every deploy
prints what goes into the image and warns about secrets; `homebase docs` shows the same for the
current folder before deploying. `homebase eject` writes the generated Dockerfile and
.dockerignore into the project so you can change them.

Docker must run on this machine to build locally; otherwise homebase builds on the server.

## Data on the server

The container is replaced on every deploy, rollback and restart, so anything written inside
it is lost. Keep data in one of two places:

- Files (uploads, SQLite): `volumes = ["/data"]` under [deploy]. Each path is a docker volume
  mounted into every release; the app gets `DATA_DIR=/data`. For SQLite, put the database file
  there and enable WAL mode. Removing a path from homebase.toml never deletes its volume.
- Postgres: `homebase db add` (works before the first deploy). The server runs one shared
  Postgres, not reachable from the internet; each app gets its own database and
  `DATABASE_URL`. Nightly backups, the last 7 are kept.

Migrations: `release = "npm run migrate"` under [deploy]. It runs once in the new image, with
the app's env and volumes, before traffic switches; if it fails (`release_command_failed`),
the old release keeps serving. It does not run on rollback or restart.

## Sharing a dev server

    homebase share [--private]

Publishes the dev server on this Mac at https://<name>.dev.<domain> through your server: the
Mac keeps an SSH tunnel open to it, and the server's proxy sends the name into the tunnel.
DNS is not touched (the wildcard record covers it). Public by default; --private requires
a secret token (a link that sets a cookie, or an X-Homebase-Token header). The app sees
Host <name>.localhost (so dev servers' host checks pass), the public host in
X-Forwarded-Host, X-Forwarded-Proto https, and the visitor's IP in X-Forwarded-For.
While the Mac sleeps, the URL shows an "offline" page (with Caddy; servers that use
nginx answer 502).

## homebase.toml

    name = "my-app"                       # project name: my-app.localhost, my-app.<domain>
    start = "pnpm dev --port $PORT"       # dev server, runs via zsh -lc in this folder
    port = 3000                           # optional: pin the dev port

    [env]                                 # dev server environment (committed: no secrets)
    API_URL = "http://api.localhost"

    [deploy]                              # all optional; the first deploy writes server
    server = "root@1.2.3.4"
    domain = "my-app.example.com"         # custom domain
    port = 3000                           # container port
    health = "/healthz"
    start = "node dist/server.js"         # start command in the image, without a Dockerfile
    dockerfile = "deploy/Dockerfile"
    build = "remote"                      # build on the server
    volumes = ["/data"]                   # persistent paths
    release = "npm run migrate"           # runs before traffic switches
    memory = "256m"                       # removing it removes the limit
    name = "my-api"                       # app name on the server, if not `name`

An old ship.toml is read as [deploy] and moved into homebase.toml on the next save.

## Commands

On this Mac:

    homebase [dir]          start the dev server (see above); `homebase start` is the same
    homebase status         this project: dev server, share, and the deployed app if any
                            (--local: no SSH; --prod: only the deployed app)
    homebase ls             all dev servers, the apps on your server, and the setup (--local)
    homebase logs [-n 50]   the dev server's output; -f follows (never returns)
    homebase open           open it in the browser
    homebase restart        restart, re-reading homebase.toml (--all for every server)
    homebase stop           stop it; it stays stopped after a reboot (--all)
    homebase forget         stop it and remove it from homebase; files are not touched
    homebase share          publish at https://<name>.dev.<domain> (--private for a token)
    homebase unshare        stop publishing
    homebase env [set K=V | unset K]   [env] in homebase.toml, then restart
    homebase init           set up this Mac (proxy; --lan)
    homebase uninstall      stop everything homebase runs here; settings are kept

On your server:

    homebase deploy [dir]           build and release (see above)
    homebase status --prod          URL, state, health, CPU/memory/disk, releases
    homebase logs --prod [-n 50] [-f] [--since 10m]
    homebase restart --prod         restart the current release (zero downtime)
    homebase open --prod            open the deployed app
    homebase rollback               back to the previous release (zero downtime)
    homebase env --prod [--reveal]  env vars, stored on the server (0600); secrets go here
    homebase env set K=V --prod     set and restart (--no-restart to skip)
    homebase env unset K --prod
    homebase db add|info            Postgres database for the app; DATABASE_URL
    homebase db shell [-c "SQL"]    psql (interactive, or one statement with -c)
    homebase db backup [--download|-o FILE]
    homebase db restore FILE --yes  replace the database (pg_dump -Fc or .sql); backup first
    homebase destroy --yes          remove the app; KEEPS volumes, database, env
    homebase destroy --data --yes   also delete them (a final database backup is kept)
    homebase eject [--force]        write the generated Dockerfile + .dockerignore
    homebase server add user@host [--install] [--domain D] [--dev-domain D] [--no-default]
                    [--wildcard cloudflare --dns-token-file FILE]   one *.D certificate
    homebase server                 the server: domains, memory, disk, apps, shares

Both:

    homebase agents install         tell coding agents on this machine about homebase
    homebase docs                   this guide plus a plan for the current folder

Every command takes --json. -a NAME picks a project; -s user@host a server.
App name on the server: -a, [deploy] name, name, or the folder name.
Server: -s, [deploy] server, $HOMEBASE_SERVER, or the one from `homebase server add`.
Domain of an app: --domain, [deploy] domain, the one it had, <name>.<server domain>, or
<name>.<ip>.sslip.io.

## URLs

    http://localhost:<port>          the dev server, always
    http://<name>.localhost          through the proxy (`homebase init`)
    http://<name>.local              other devices on the network (`homebase init --lan`)
    https://<name>.dev.<domain>      a shared dev server, from anywhere (`homebase share`)
    https://<name>.<domain>          the deployed app

## JSON output

With --json stdout is exactly one object; progress goes to stderr:

    {"ok": true, "data": {...}}
    {"ok": false, "error": {"code": "...", "message": "...", "hint": "...", "logs": "..."}}

Dev server objects (homebase, status, share) have: name, state (running|stopped|crashed|
exited), last_exit, pid, uptime, port, listening, url, local_url, lan_url, public_url,
shared (public|private), share_link, dir, command, env, log. `status` adds `prod` (the
deployed app, as `status --prod` returns it) or `prod_error` when the project has a [deploy]
table. `homebase ls` returns {"servers": [...], "machine": {...}, "deployed": {...}}.

Deploy result: url, tls, release.id, warnings[] (e.g. the certificate is not issued yet
because DNS does not point at the server), context (what went into the image).
`status --prod`: url, state, healthy, restarts, oom_kills (> 0: raise `memory`), current and
previous release, database, volumes, resources (cpu_percent, mem_bytes, mem_limit_bytes,
volume_bytes, database_bytes).

## Errors

    code                    exit  meaning
    usage                   2     wrong flags or arguments
    confirmation_required   2     run it again with --yes
    config                  3     settings or homebase.toml problem, or no server yet
    stack_not_detected      3     pass --start '<cmd>' (or add a Dockerfile to deploy)
    dependencies_missing    3     run the install command in the hint
    server_not_ready        3     the server lacks Docker or a proxy: server add --install
    share_taken             3     another Mac (or an app) has that name; rename the project
    build_failed            4     docker build failed; see logs
    upload_failed           4     the image didn't reach the server
    port_in_use             5     another program holds the port: --port N
    start_failed            5     the dev server's command exited; see logs
    not_listening           5     it runs, but not on $PORT
    launchd_failed          5     macOS refused to start the agent
    container_failed        5     the app crashed on start or didn't listen on $PORT
    health_check_failed     5     the app didn't answer in time; the old release still serves
    release_command_failed  5     the release command failed; the old release still serves
    proxy_config_failed     5     the server's proxy rejected the route
    database_failed         5     database operation failed
    deploy_in_progress      5     another deploy of the app runs; retry
    ssh_failed              6     `ssh user@host` must work without a password prompt
    server_not_found        7     no dev server here: run `homebase`, or pass -a NAME
    app_not_found           7     not deployed on the server: `homebase deploy`
    no_previous_release     7     nothing to roll back to
    internal                1     anything else

## Rules for agents

- Prefer `homebase --json` over starting dev servers yourself, and `homebase deploy` over
  manual ssh/docker/nginx work.
- Never run `homebase logs -f`; use `homebase logs -n 100` (or `--prod`).
- Secrets go to the server with `homebase env set KEY=value --prod`; never put them in
  homebase.toml, which is committed.
- Ask before `homebase share`, `init`, `uninstall` and `server add`: they publish things on
  the internet or change the whole machine. Share links with a token are secrets.
- `homebase destroy` requires --yes and keeps data; only add --data when the user explicitly
  asks to delete the app's data.
- Leave dev servers running when you are done unless the user wants them stopped.

## Files

    homebase.toml                         per project, meant to be committed
    ~/.config/homebase/servers.yaml       registry: folders, ports, shares, server (0600)
    ~/Library/LaunchAgents/dev.homebase.* launch agents
    ~/Library/Logs/homebase/<name>.log    logs (also proxy.log, tunnel.log)

On the server: /usr/local/bin/shipd (homebase's helper, installed and updated
automatically), state in /var/lib/ship, one Caddy file per app in /etc/caddy/ship/,
containers ship-<app>-<release> on 127.0.0.1:20000-29999, volumes ship-<app>-*.
