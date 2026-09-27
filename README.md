<p align="center">
  <img src="docs/logo.svg" width="160" alt="ship logo">
</p>

<h1 align="center">ship</h1>

<p align="center">
  <b>Deploy web apps to your own server with one command.</b><br>
  No registry, no control panel, no YAML. Made for people and for AI coding agents.
</p>

```sh
$ ship init root@203.0.113.10 --install     # once per server
$ cd my-app && ship
→ Deploying my-app to root@203.0.113.10 (node, port 3000)
→ Building image for linux/amd64
→ Uploading image (48.2 MB uncompressed)
→ Releasing
✓ my-app is live at https://my-app.203-0-113-10.sslip.io
```

## Why

You have a VPS and an app. You want it online with HTTPS, you want the next deploy not to
take the site down, and you don't want to learn Kubernetes or run a PaaS for it.

- **Just SSH.** ship talks to your server over your normal `ssh`. It installs a small helper
  (`shipd`) there and keeps it up to date. Nothing listens on extra ports.
- **No Dockerfile needed.** Node, Python (Django, FastAPI, Flask), Go, Rust and static sites are
  detected; if you have a Dockerfile, it is used as is.
- **HTTPS automatically.** [Caddy](https://caddyserver.com) gets and renews certificates. Without
  a domain you get a working `https://<app>.<ip>.sslip.io` address right away.
- **Zero-downtime deploys.** The new version must answer HTTP before traffic switches to it.
  A broken build never replaces a working one, and `ship rollback` switches back instantly.
- **Data that survives deploys.** Persistent volumes for uploads and SQLite, a Postgres database
  per app with nightly backups, and a release command for migrations.
- **Built for agents.** Never interactive, `--json` everywhere, stable error codes with hints and
  logs, and `ship docs`, a guide written so that an agent can deploy a project it has never seen.

## Install

**macOS** with [Homebrew](https://brew.sh):

```sh
brew install semenov/tap/ship
```

**Linux** (amd64 or arm64), from the [latest release](https://github.com/semenov/ship/releases/latest):

```sh
arch=$(uname -m | sed 's/x86_64/amd64/;s/aarch64/arm64/')
curl -fsSL https://github.com/semenov/ship/releases/latest/download/ship_linux_${arch}.tar.gz \
  | sudo tar -xz -C /usr/local/bin ship
```

**From source** (Go 1.23+ and make):

```sh
git clone https://github.com/semenov/ship.git
cd ship
make install          # installs to ~/.local/bin/ship (change with BINDIR=...)
```

To build images on your machine you also need Docker (Docker Desktop, OrbStack, Colima...).
Without Docker, ship builds on the server instead.

## Quick start

**1. Prepare a server** (once). Any Ubuntu or Debian VPS you can `ssh` into as root, or as a
user with passwordless sudo:

```sh
ship init root@203.0.113.10 --install
```

This installs Docker and Caddy if they are missing and makes the server your default.
It is safe to run again.

**2. Deploy** from your project directory:

```sh
cd my-app
ship
```

The first deploy writes a small `ship.toml` (app name and server). Commit it; after that, every
deploy is just `ship`.

**3. Live with it:**

```sh
ship status              # URL, health, CPU/memory, current and previous release
ship logs -f             # follow the logs
ship rollback            # back to the previous release
ship env set API_KEY=…   # set env vars (the app restarts with zero downtime)
ship ls                  # all apps on the server, with resource usage
```

## Domains and HTTPS

| You want | Do this |
|---|---|
| Just a working URL | Nothing. Apps get `https://<app>.<ip-with-dashes>.sslip.io` |
| Your own domain for one app | Point an `A` record at the server, then `ship --domain app.example.com` |
| `<app>.example.com` for every app | Add a wildcard record `*.example.com → server IP`, then `ship init <server> --base-domain example.com` |
| Instant HTTPS for new apps | With the DNS on Cloudflare: `ship init <server> --base-domain example.com --wildcard cloudflare --dns-token-file token.txt` |

With `--wildcard`, one `*.example.com` certificate covers every app, so new apps need no
certificate request at all (no waiting, no Let's Encrypt rate limits). The token needs
permission to edit DNS records of the zone.

## Data: files and databases

The app's container is replaced on every deploy, so anything written inside it is lost.
Keep data in one of these places:

**Files and SQLite:** a persistent volume.

```toml
# ship.toml
volumes = ["/data"]        # the app gets DATA_DIR=/data
```

**Postgres:** one command, before or after the first deploy.

```sh
ship db add              # creates a database, sets DATABASE_URL, restarts the app
ship db shell            # psql (or: ship db shell -c "select count(*) from users")
ship db backup --download
ship db restore dump.sql --yes
```

All apps on a server share one Postgres container, and each app gets its own database and user.
It is not reachable from the internet. Backups run every night (the last 7 are kept).

**Migrations:** a release command runs in the new version before traffic switches to it.
If it fails, the old version keeps running.

```toml
release = "npm run migrate"
```

## Using ship with AI agents

Tell your agent: *"deploy this with ship"*. On its own it will read `ship --help`, then
`ship docs` (the full guide plus a plan for the current project: detected stack, port, what data
the app needs), and deploy.

To make every agent session on your machine know about ship without being told:

```sh
ship agents install      # Claude Code (as a skill), Codex, OpenCode, Gemini CLI
```

This adds a short, clearly marked note to each agent's global instructions. `ship agents
uninstall` removes it again.

For scripts and agents, every command supports `--json`: stdout is always exactly one object,
`{"ok":true,"data":…}` or `{"ok":false,"error":{"code","message","hint","logs"}}`, and the exit
code depends on the kind of error. See `ship docs` for the list.

## ship.toml

Every field is optional; the first deploy writes `name` and `server`.

```toml
name = "my-app"
server = "root@203.0.113.10"
domain = "app.example.com"      # custom domain
port = 3000                     # port the app listens on (default: detected)
health = "/healthz"             # must answer non-5xx before traffic switches (default "/")
start = "node dist/server.js"   # start command when there is no Dockerfile
dockerfile = "deploy/Dockerfile"
build = "remote"                # build on the server instead of locally
volumes = ["/data"]             # persistent paths
release = "npm run migrate"     # runs before traffic switches
memory = "256m"                 # memory limit; the app restarts if it exceeds it
```

The app always gets `PORT` and must listen on `0.0.0.0:$PORT`.

## Commands

| Command | What it does |
|---|---|
| `ship [dir]` | Build and deploy (`--domain`, `--port`, `--volume`, `--memory`, `--release`, `--remote-build`) |
| `ship init user@host` | Prepare a server and make it the default (`--install`, `--base-domain`, `--wildcard`) |
| `ship status` / `ship ls` | Health, URL, releases, CPU/memory/disk of one app / all apps |
| `ship logs [-f] [-n 100]` | Container logs |
| `ship rollback` / `ship restart` | Previous release / fresh container, both with zero downtime |
| `ship env ls\|set\|unset` | Environment variables, stored on the server |
| `ship db add\|info\|shell\|backup\|restore` | Postgres database for the app |
| `ship destroy --yes` | Remove the app; volumes and database are kept unless `--data` |
| `ship agents install\|uninstall\|status` | Tell the coding agents on this machine about ship |
| `ship docs` | The full guide, plus a summary of the current project |

Global flags: `--json`, `-a/--app NAME`, `-s/--server user@host`.

## How it works

```
your machine                                  your server
────────────                                  ───────────
ship ──── docker build (linux/amd64|arm64)
     ──── docker save | gzip ──── ssh ────▶  docker load
     ──── ssh ───────────────────────────▶  shipd deploy
                                              ├─ start the new container on 127.0.0.1:20000+
                                              ├─ wait until it answers HTTP
                                              ├─ run the release command (if any)
                                              ├─ point Caddy at it (caddy reload, atomic)
                                              └─ stop the old container
```

What ends up on the server:

| Path | What |
|---|---|
| `/usr/local/bin/shipd` | the helper, installed and updated by ship |
| `/var/lib/ship/` | app state, env vars (mode 0600), database backups |
| `/etc/caddy/ship/<app>.caddy` | one route per app, imported from `/etc/caddy/Caddyfile` |
| Docker | containers `ship-<app>-<release>`, images `ship/<app>` (current and previous), volumes `ship-<app>-*`, `ship-postgres` |

Servers that already run nginx on port 443 are supported too: ship then writes
`ship-<app>.conf` files, checks every change with `nginx -t` (and reverts it if invalid) and
gets certificates with certbot.

## Security notes

- ship connects with your SSH keys and needs root or passwordless sudo on the server; `shipd`
  runs as root.
- App containers only listen on `127.0.0.1`; Caddy is the only thing exposed (ports 80 and 443).
- Env vars and database credentials are stored on the server in files readable only by root.
- The DNS API token for `--wildcard` is stored in `/etc/caddy/dns.env` (root only).

## Limitations

- One server per app (no clustering or load balancing across servers).
- `--install` supports Debian and Ubuntu. Elsewhere, install Docker and Caddy yourself.
- Wildcard certificates support Cloudflare DNS for now.
- Postgres is the only managed database so far.

## Development

```sh
make          # builds the linux shipd helpers, embeds them and builds dist/ship
make test
make release VERSION=x.y.z   # archives for all platforms in dist/release
```

- `cmd/ship`, `internal/cli`: the client
- `cmd/shipd`, `internal/agent`: the server helper
- `internal/detect`: stack detection and Dockerfile generation
- `internal/proto`: the JSON protocol and error codes shared by both

## License

[MIT](LICENSE)
