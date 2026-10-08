<p align="center">
  <img src="docs/logo.svg" width="140" alt="homebase logo: a house with a server rack inside, broadcasting signal waves">
</p>

<h1 align="center">homebase</h1>

<p align="center">
  <b>Your projects: dev servers on your Mac, deploys to your own server.</b><br>
  Real URLs for both, one command each. Made for people and for AI coding agents.
</p>

```
$ cd my-app && homebase
  → Detected Vite: pnpm dev --port $PORT --strictPort
  → Wrote homebase.toml (commit it: anyone can now run `homebase` here)

  ● my-app is running

    URL       http://my-app.localhost
    Network   http://my-app.local
    Port      4001
    Process   74678, up 2s
    Command   pnpm dev --port $PORT --strictPort
    Logs      ~/Library/Logs/homebase/my-app.log

$ homebase share
  ● my-app is public

    URL       https://my-app.dev.example.com

$ homebase deploy
  → Deploying my-app to root@203.0.113.10 (node-static, port 80)
  → Building the image for linux/amd64
  → Releasing

  ● my-app is live

    URL       https://my-app.example.com
```

## Why

You have a handful of projects. Each needs a dev server running somewhere, and the finished
ones need to be online. That usually means a terminal tab per project, ports you have to
remember, servers that stop when an agent session ends, and a separate setup for production.

- **One command for dev.** `homebase` works out how to start the project (Next.js, Vite, Django,
  FastAPI, Rails, Go and more), picks a free port and starts it as a launchd agent. It keeps
  running after you close the terminal, restarts after a crash and comes back after a reboot.
- **One command for prod.** `homebase deploy` builds a Docker image (from your Dockerfile, or one
  generated for the stack), uploads it to your server over SSH and releases it behind Caddy with
  HTTPS. No registry, no control panel. The new version must answer before traffic switches, so
  a broken build never replaces a working one.
- **Real URLs.** `http://my-app.localhost` on your Mac, `http://my-app.local` on your phone,
  `https://my-app.dev.example.com` for a dev server you share, and `https://my-app.example.com`
  in production.
- **Your own server, any DNS.** One wildcard DNS record at any provider, set up once. Sharing and
  deploying never touch DNS. Without a domain, `sslip.io` addresses work right away.
- **Settings live in the project.** `homebase.toml` holds how to start the dev server and how
  to deploy. Commit it, and a teammate or an agent with a fresh clone only has to run `homebase`.
- **Built for agents.** Never interactive, `--json` everywhere, stable error codes with hints
  and logs, and `homebase docs`: a guide plus a plan for the current folder.

## Install

With [Homebrew](https://brew.sh):

```sh
brew install semenov/tap/homebase
homebase init          # once: the proxy behind http://<name>.localhost
```

On Linux, homebase can deploy (everything under [On your server](#on-your-server)); dev servers
need macOS for now. Archives for Linux are on the
[releases page](https://github.com/semenov/homebase/releases/latest).

From source (Go and make): `make install`. It builds homebase's server helper first, which
homebase embeds.

## On your Mac

```sh
cd my-app
homebase               # start it (or: homebase start)
homebase status        # state, URLs, port, logs; and the deployed app, if any
homebase logs -f       # follow its output
homebase open          # open it in the browser
homebase restart       # restart, re-reading homebase.toml
homebase stop          # stop it; it stays stopped after a reboot
homebase forget        # stop it and remove it from homebase; your files stay
homebase ls            # every dev server, and the apps on your server
```

Commands act on the project of the current folder, and they work from its subfolders too.
From anywhere else, name it: `homebase -a my-app logs`.

When something goes wrong, homebase tells you what happened and shows the server's output:

```
  ✗ my-app crashed while starting (exit 1)  [start_failed]

    │ Error: Cannot find module 'express'

    fix the error above, then run `homebase` again (the command is in homebase.toml)
```

## On your server

Any Ubuntu or Debian VPS you can `ssh` into as root, or as a user with passwordless sudo:

```sh
homebase server add root@203.0.113.10 --install --domain example.com
```

This installs Docker and Caddy if they are missing and makes the server your default. Then add
one DNS record at your DNS provider: `*.example.com → 203.0.113.10` (type A; if the provider is
Cloudflare, not proxied). It covers both the apps (`<name>.example.com`) and shared dev servers
(`<name>.dev.example.com`). Without `--domain`, apps get `https://<name>.203-0-113-10.sslip.io`,
which needs no DNS at all.

```sh
cd my-app
homebase deploy                 # build and release it
homebase status --prod          # URL, health, CPU/memory/disk, releases
homebase logs --prod -f         # the container's output
homebase rollback               # back to the previous release
homebase env set API_KEY=… --prod   # secrets live on the server; the app restarts
homebase db add                 # a Postgres database; the app gets DATABASE_URL
homebase server                 # the server: domains, memory, disk, apps, shares
```

Commands that exist on both sides (`status`, `logs`, `open`, `restart`, `env`) act on your Mac;
`--prod` sends them to the server. Deploying never happens by accident: only `homebase deploy`
deploys.

The container is replaced on every deploy, so keep data in one of two places:

- **Files and SQLite:** `volumes = ["/data"]` under `[deploy]`; the app gets `DATA_DIR=/data`.
- **Postgres:** `homebase db add`. One Postgres container on the server, a database per app,
  nightly backups (the last 7 are kept), `homebase db shell`, `db backup --download`,
  `db restore`.

Migrations: `release = "npm run migrate"` under `[deploy]` runs in the new image before traffic
switches; if it fails, the old release keeps serving.

### What gets built

| Project | Image |
|---|---|
| `Dockerfile` | used as is; the port from its `EXPOSE` |
| `package.json` | node:22; installs by lockfile, runs `build`, starts with `start`. No `start` but a `build`: a static SPA served by nginx |
| `requirements.txt` / `pyproject.toml` | python:3.12; Django and Flask with gunicorn, FastAPI with uvicorn |
| `go.mod` | a static binary on alpine |
| `Cargo.toml` | `cargo build --release`, run on debian-slim, dependencies cached in their own layer |
| `index.html` | a static site on nginx; hidden files and `*.md` stay out |

Every deploy shows what goes into the image and warns about secrets like `.env`.
`homebase eject` writes the generated Dockerfile into the project when you want control.

## Sharing a dev server

```sh
homebase share             # → https://my-app.dev.example.com
homebase share --private   # needs a secret token: a link that sets a cookie, or a header
homebase unshare
```

Your Mac keeps an SSH tunnel open to your server, and the server's Caddy sends
`<name>.dev.example.com` into it. Your Mac's address stays hidden, there is no request time
limit or upload cap, and while the Mac sleeps the URL shows an "offline" page.

- **What the app sees:** `Host: my-app.localhost` (so dev servers' host checks pass), the
  public host in `X-Forwarded-Host`, `X-Forwarded-Proto: https`, and the visitor's IP in
  `X-Forwarded-For`.
- **Names are unique.** The server hands out the names, so a shared dev server can never take
  the name of a deployed app, and two Macs can't share the same name.
- **Private shares:** open the printed link once per browser; apps and scripts send the token in
  an `X-Homebase-Token` header. Your app never sees the token.

## homebase.toml

The first run writes it, the first deploy adds `[deploy]`. Every field is optional.

```toml
name = "my-app"                    # my-app.localhost, my-app.example.com
start = "pnpm dev --port $PORT"    # dev server: runs via `zsh -lc` in this folder
port = 3000                        # pin the dev port (normally picked per machine)

[env]
API_URL = "http://api.localhost"   # dev server environment; committed, so no secrets

[deploy]
server = "root@203.0.113.10"
domain = "app.example.com"         # a custom domain instead of my-app.example.com
port = 3000                        # the port the app listens on in the container
health = "/healthz"                # must answer before traffic switches (default /)
volumes = ["/data"]                # persistent paths
release = "npm run migrate"        # runs before traffic switches
memory = "256m"                    # memory limit
```

The dev server gets its port in `$PORT`; the deployed app listens on `0.0.0.0:$PORT`. Flags
override the file and update it: `homebase --start '...'`, `--port N`, `--env KEY=VALUE`
(`--no-save` leaves the file alone). Put `--start` in single quotes, so that `$PORT` is
expanded by the server's shell rather than yours.

### What gets detected for dev

| Project | Command |
|---|---|
| `package.json` | the `dev` (or `start`) script, run with npm, pnpm, yarn or bun depending on the lockfile. Adds port flags for Next.js, Vite (SvelteKit, Remix, React Router), Astro, Nuxt, Angular, webpack, Gatsby; Create React App and plain Node read `$PORT` |
| `manage.py` | `python manage.py runserver 127.0.0.1:$PORT` |
| FastAPI / Flask | `uvicorn main:app --reload --port $PORT` / `flask --app app run --debug --port $PORT` |
| Rails | `bin/rails server -p $PORT` |
| `go.mod`, `Cargo.toml`, `deno.json` | `go run .`, `cargo run`, `deno task dev` (the app must read `$PORT`) |
| `index.html` | `python3 -m http.server $PORT` |

Python commands go through `uv run` or a local `.venv` when the project has one. If a Node
project has no `node_modules`, homebase stops and shows the install command instead of
starting a server that would crash.

## URLs

| URL | What | Set up with |
|---|---|---|
| `http://localhost:<port>` | the dev server, on this Mac | nothing |
| `http://<name>.localhost` | the dev server, on this Mac | `homebase init` |
| `http://<name>.local` | the dev server, from phones and computers on your network | `homebase init --lan` |
| `https://<name>.dev.<domain>` | a shared dev server, from anywhere | `homebase server add`, then `homebase share` |
| `https://<name>.<domain>` | the deployed app | `homebase server add`, then `homebase deploy` |

- **`*.localhost` needs no DNS setup:** macOS resolves it to 127.0.0.1 itself. The proxy listens
  on port 80 without root (macOS allows that on all interfaces) and, unless LAN mode is on,
  rejects anything that doesn't come from this Mac. It keeps the browser's `Host` header and
  passes WebSockets through, so hot reload works. `http://localhost` lists all dev servers.
- **`.local` names** are announced over Bonjour through the Mac's own mDNSResponder, on the Wi-Fi
  or Ethernet address (never a VPN's). iOS and macOS resolve them natively; networks that keep
  devices apart (many guest and office Wi-Fi networks) block it. It is plain HTTP, so phone
  browsers turn off features that need a secure context; share the server when you need those.

## Using homebase with AI agents

Tell your agent *"run this with homebase"* or *"deploy this with homebase"*. It will read
`homebase --help` and then `homebase docs`: the full guide plus a plan for the current folder
(the detected dev command, missing dependencies, how the image would be built, which server).

To make every agent session know about homebase without being told:

```sh
homebase agents install   # Claude Code (as a skill), Codex, OpenCode, Gemini CLI
```

This adds a short, clearly marked note to each agent's global instructions.
`homebase agents uninstall` removes it.

For scripts and agents, every command takes `--json`:
- **Output:** stdout is exactly one object, either `{"ok":true,"data":…}` or
  `{"ok":false,"error":{"code","message","hint","logs"}}`. Progress goes to stderr.
- **Exit codes:** they depend on the kind of error; `homebase docs` lists them.
- **Waiting:** starting a dev server waits until its port is open, and a deploy until the new
  release answers, so a single command tells you whether it works.

## Commands

| Command | What it does |
|---|---|
| `homebase [dir]` | Detect, start and wait for the dev server (`--start`, `--port`, `--env`, `--no-save`, `--timeout`). Same as `homebase start` |
| `homebase status [--prod\|--local]` | This project: dev server, share, and the deployed app |
| `homebase ls [--local]` | Every dev server, the apps on your server, and the setup |
| `homebase logs [-f] [-n 50] [--prod]` | The dev server's (or the app's) output. `logs proxy` and `logs tunnel` show homebase's own |
| `homebase open [--prod]` | Open it in the browser |
| `homebase restart [--prod]` / `stop` | Restart, re-reading homebase.toml / stop. `--all` for every dev server |
| `homebase forget` | Stop the dev server and remove it from homebase. Files are not touched |
| `homebase share [--private]` / `unshare` | Publish the dev server at `https://<name>.dev.<domain>` / stop |
| `homebase env [set K=V \| unset K] [--prod]` | `[env]` in homebase.toml, or the app's variables on the server |
| `homebase init [--lan]` | Set up this Mac. Safe to run again |
| `homebase uninstall` | Stop everything homebase runs on this Mac (do this before `brew uninstall`). Settings are kept |
| `homebase deploy [dir]` | Build and release (`--domain`, `--port`, `--volume`, `--memory`, `--release`, `--remote-build`) |
| `homebase rollback` | Back to the previous release |
| `homebase db add\|info\|shell\|backup\|restore` | Postgres for the app |
| `homebase destroy --yes [--data]` | Remove the app from the server; data is kept unless `--data` |
| `homebase eject` | Write the generated Dockerfile and .dockerignore into the project |
| `homebase server add user@host` / `server` | Set up a server (`--install`, `--domain`, `--dev-domain`, `--wildcard`) / see how it does |
| `homebase agents install\|uninstall\|status` | Tell your coding agents about homebase |
| `homebase docs` | The full guide, plus a plan for the current folder |

Global flags: `--json`, `-a/--app NAME`, `-s/--server user@host`.

## How it works

On your Mac:

| What | Where |
|---|---|
| A dev server | launch agent `~/Library/LaunchAgents/dev.homebase.server.<name>.plist`, running `zsh -lc "<start>"` in the project folder with `PORT` set |
| Its output | `~/Library/Logs/homebase/<name>.log` |
| The proxy and the tunnel | launch agents `dev.homebase.proxy` and `dev.homebase.tunnel` (only while something is shared), with logs in `proxy.log` and `tunnel.log` |
| Registry: folders, ports, shares, the server | `~/.config/homebase/servers.yaml` (mode 0600; `$HOMEBASE_CONFIG` overrides the path) |
| Per-project settings | `homebase.toml`, meant to be committed |

On your server, everything goes through `ssh`, so your keys, `~/.ssh/config` and ssh-agent work
as usual and nothing listens on extra ports:

```
your Mac                                        your server
────────                                        ───────────
homebase deploy ── docker build (linux/amd64|arm64)
                ── docker save | gzip ── ssh ─▶ docker load
                ── ssh ─────────────────────▶  shipd deploy
                                                 ├─ start the new container on 127.0.0.1:20000+
                                                 ├─ wait until it answers HTTP
                                                 ├─ run the release command (if any)
                                                 ├─ point Caddy at it (caddy reload, atomic)
                                                 └─ stop the old container

homebase share ── ssh -R ──────────────────▶  127.0.0.1:30000+ ◀─ Caddy ◀─ https://<name>.dev.<domain>
```

| On the server | What |
|---|---|
| `/usr/local/bin/shipd` | homebase's helper, installed and updated by homebase |
| `/var/lib/ship/` | app state, env vars (mode 0600), shares, database backups |
| `/etc/caddy/ship/<app>.caddy` | one route per app (and `_dev-<name>.caddy` per share), imported from `/etc/caddy/Caddyfile` |
| Docker | containers `ship-<app>-<release>`, images `ship/<app>` (current and previous), volumes `ship-<app>-*`, `ship-postgres` |

The names on the server come from ship, homebase's deploy half before the two merged; servers
set up by ship keep working as they are. If a server already runs nginx on port 443, homebase uses
it instead of Caddy: it writes `ship-<app>.conf` files, checks every change with `nginx -t` and
gets certificates with certbot. With `--wildcard cloudflare` (and a DNS API token), Caddy gets
one `*.<domain>` certificate, so new apps have HTTPS instantly.

## Coming from ship

homebase now includes ship. `homebase deploy` is `ship`, and the other commands keep their names
(`homebase status --prod` for `ship status`, `homebase logs --prod` for `ship logs`,
`homebase env … --prod` for `ship env`, `homebase server add` for `ship init`). homebase picks up
your default server from ship's config, reads `ship.toml` as `[deploy]` and moves it into
`homebase.toml` on the next deploy or run. Deployed apps keep running; nothing needs a redeploy.

## Development

```sh
make build                    # dist/homebase, with the server helper for linux/amd64 and arm64
make test
make release VERSION=x.y.z    # archives for the Homebrew cask in dist/release
```

- `internal/cli`: commands and output
- `internal/detect`: how to start a project (dev) and how to build its image (deploy)
- `internal/config`: the registry and homebase.toml
- `internal/launchd`, `internal/proxy`, `internal/bonjour`: the machinery on the Mac
- `internal/agent` (`cmd/shipd`), `internal/proto`: the helper on the server and its protocol

## License

[MIT](LICENSE)
