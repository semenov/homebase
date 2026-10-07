<p align="center">
  <img src="docs/logo.svg" width="140" alt="homebase logo: a house with a server rack inside, broadcasting signal waves">
</p>

<h1 align="center">homebase</h1>

<p align="center">
  <b>Local dev servers on your Mac, with real URLs.</b><br>
  One command in a project folder. Made for people and for AI coding agents.
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
```

## Why

You have a handful of projects, each with a dev server. Every one of them needs a terminal
tab and a port you have to remember, and it stops when the tab closes or an agent session ends.

- **One command.** `homebase` works out how to start the project (Next.js, Vite, Django,
  FastAPI, Rails, Go and more), picks a free port and starts it. You can run it again any time;
  it only restarts the server when something changed.
- **Runs in the background.** Every server is a launchd agent. It keeps running after you close
  the terminal or an agent session ends, restarts after a crash and comes back after a reboot.
- **Real URLs.** `http://my-app.localhost` on your Mac, `http://my-app.local` on your phone,
  and `https://my-app.example.com` from anywhere when you share it through Cloudflare.
- **Settings live in the project.** The first run writes a small `homebase.toml`. Commit it,
  and a teammate or an agent with a fresh clone only has to run `homebase`.
- **Built for agents.** Never interactive, `--json` everywhere, stable error codes with hints
  and the server's log output, and `homebase docs`: a guide plus a plan for the current folder.

## Install

With [Homebrew](https://brew.sh):

```sh
brew install semenov/tap/homebase
homebase init          # once: the proxy behind http://<name>.localhost
```

From source: `go install github.com/semenov/homebase@latest`.

## Using it

```sh
cd my-app
homebase               # start it (or: homebase start)
homebase status        # state, URLs, port, logs
homebase logs -f       # follow its output
homebase open          # open it in the browser
homebase restart       # restart, re-reading homebase.toml
homebase stop          # stop it; it stays stopped after a reboot
homebase forget        # stop it and remove it from homebase; your files stay
homebase ls            # every server on this Mac
```

Commands act on the server of the current folder, and they work from its subfolders too.
From anywhere else, name the server: `homebase -a my-app logs`.

When something goes wrong, homebase tells you what happened and shows the server's output:

```
  ✗ my-app crashed while starting (exit 1)  [start_failed]

    │ Error: Cannot find module 'express'

    fix the error above, then run `homebase` again (the command is in homebase.toml)
```

## homebase.toml

The first run writes this file. Every field is optional. To apply an edit, run `homebase`
again (or `homebase restart`).

```toml
name = "my-app"                    # server name and hostname: my-app.localhost
start = "pnpm dev --port $PORT"    # runs via `zsh -lc` in this folder, with your PATH
port = 3000                        # pin a port (normally picked per machine)

[env]
API_URL = "http://api.localhost"   # extra environment; not for secrets
```

The server gets its port in `$PORT` and must listen on it. These flags override the file
and update it: `homebase --start '...'`, `--port N`, `--env KEY=VALUE`. Add `--no-save` to
leave the file as it is. Put the `--start` command in single quotes, so that `$PORT` is
expanded by the server's shell rather than yours.

### What gets detected

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

| URL | Where it works | Set up with |
|---|---|---|
| `http://localhost:<port>` | this Mac | nothing |
| `http://<name>.localhost` | this Mac | `homebase init` |
| `http://<name>.local` | phones and computers on your network | `homebase init --lan` |
| `https://<name>.<domain>` | anywhere | `homebase init --tunnel <domain>`, then `homebase share` |

You can run `homebase init` again at any time. With flags it changes the setup
(`--no-lan` and `--no-tunnel` turn things off again); without flags it makes sure everything
is running. `homebase ls` shows the current setup.

### `*.localhost`

- **No DNS setup.** macOS resolves `*.localhost` to 127.0.0.1 itself, so you don't need
  `/etc/hosts` or sudo.
- **Port 80 without root.** macOS allows that when the proxy listens on all interfaces.
  Unless LAN mode is on, it rejects anything that doesn't come from this Mac.
- **Hot reload works.** The proxy keeps the browser's `Host` header and passes WebSockets through.
- **Index page.** `http://localhost` lists all servers.

### Your network (`--lan`)

- **Announcing names.** The proxy announces `<name>.local` over Bonjour, through the Mac's own
  mDNSResponder.
- **Which IP.** It uses the Wi-Fi or Ethernet address (never a VPN tunnel's) and follows IP changes.
- **Access.** While LAN mode is on, anyone on the network can open your servers. Without it,
  the proxy only answers this Mac.
- **Where it works.** iOS and macOS resolve `.local` names natively. Networks that keep
  devices apart (many guest and office Wi-Fi networks) block it.
- **Plain HTTP.** Phone browsers turn off features that need a secure context, such as service
  workers and the camera. Use a tunnel when you need those.

### From anywhere (`--tunnel`)

You need a domain whose DNS is managed by Cloudflare. The free plan is enough.

```sh
brew install cloudflared
cloudflared tunnel login                  # once, in the browser: pick your domain
homebase init --tunnel example.com        # creates the tunnel "homebase" and runs it
cd my-app && homebase share               # → https://my-app.example.com
```

```
browser ── https://my-app.example.com ──▶ Cloudflare ──tunnel──▶ cloudflared (your Mac)
        ──▶ homebase proxy (127.0.0.1:8780) ──▶ my-app on localhost:4001
```

Nothing is reachable until you `share` it. For everything else, the proxy answers 404.

- **Public by default.** That's the right choice for backends with their own login, mobile
  apps and webhooks.
- **`homebase share --private`** requires a secret token:
  - **Browsers:** open the printed link once per browser; it sets a cookie.
  - **Apps and scripts:** send the token in an `X-Homebase-Token` header.
  - **Your app never sees the token.**
  - `--new-token` replaces the token, and `--public` opens the server up again.
  - For real logins, put [Cloudflare Access](https://developers.cloudflare.com/cloudflare-one/applications/configure-apps/self-hosted-apps/)
    in front of a public share.
- **`homebase unshare`** makes the URL return 404. The DNS record stays in Cloudflare,
  because cloudflared can't delete records.
- **DNS.**
  - `share` never overwrites an existing record.
  - A wildcard record (`*.example.com`) doesn't block it, though, so the name stops going
    wherever the wildcard pointed. `share` warns you when that happens.
  - Cloudflare's free certificate covers one level of subdomain: `my-app.example.com` works,
    but `my-app.dev.example.com` needs Advanced Certificate Manager.
- **What the app sees:**
  - `Host: my-app.localhost`, so dev servers' host checks pass;
  - the public host in `X-Forwarded-Host`, and `X-Forwarded-Proto: https`;
  - the visitor's IP in `X-Forwarded-For`.
- **Cloudflare's limits:** requests time out after 100 seconds, and uploads are capped at 100 MB.

## Using homebase with AI agents

Tell your agent *"run this with homebase"*. It will read `homebase --help` and then
`homebase docs`, which is the full guide plus a plan for the current folder. The plan covers
the detected command, any missing dependencies, and whether a server is already running.
Then the agent starts the server.

To make every agent session on your Mac know about homebase without being told:

```sh
homebase agents install   # Claude Code (as a skill), Codex, OpenCode, Gemini CLI
```

This adds a short, clearly marked note to each agent's global instructions.
`homebase agents uninstall` removes it.

For scripts and agents, every command takes `--json`:
- **Output:** stdout is exactly one object, either `{"ok":true,"data":…}` or
  `{"ok":false,"error":{"code","message","hint","logs"}}`.
- **Exit codes:** they depend on the kind of error; `homebase docs` lists them.
- **Waiting:** starting a server waits until its port is open, so a single command tells
  you whether it works.

## Commands

| Command | What it does |
|---|---|
| `homebase [dir]` | Detect, start and wait for the project's server (`--start`, `--port`, `--env`, `--no-save`, `--timeout`). Same as `homebase start` |
| `homebase status` / `ls` | One server in detail / every server plus the machine setup |
| `homebase logs [-f] [-n 50]` | The server's output. `logs proxy` and `logs tunnel` show homebase's own |
| `homebase open` | Open it in the browser |
| `homebase restart` / `stop` | Restart, re-reading homebase.toml / stop. `--all` for every server |
| `homebase forget` | Stop the server and remove it from homebase. The project's files are not touched |
| `homebase share [--private]` / `unshare` | Publish at `https://<name>.<domain>` / stop publishing |
| `homebase init [--lan] [--tunnel DOMAIN]` | Set up this Mac. Safe to run again |
| `homebase uninstall` | Stop everything homebase runs (do this before `brew uninstall`). Settings are kept |
| `homebase agents install\|uninstall\|status` | Tell the coding agents on this Mac about homebase |
| `homebase docs` | The full guide, plus a plan for the current folder |

Global flags: `--json`, `-a/--app NAME`.

## How it works

| What | Where |
|---|---|
| A server | launch agent `~/Library/LaunchAgents/dev.homebase.server.<name>.plist`, running `zsh -lc "<start>"` in the project folder with `PORT` set |
| Its output | `~/Library/Logs/homebase/<name>.log` |
| The proxy and the tunnel | launch agents `dev.homebase.proxy` and `dev.homebase.tunnel`, with logs in `proxy.log` and `tunnel.log` |
| Registry: folders, ports, shares | `~/.config/homebase/servers.yaml` (mode 0600; `$HOMEBASE_CONFIG` overrides the path) |
| Per-project settings | `homebase.toml`, meant to be committed |

launchd restarts a server after a crash. `stop` disables the server's launch agent, so
stopped servers stay stopped after a reboot and running ones come back.

## Development

```sh
make build                    # dist/homebase
make release VERSION=x.y.z    # archives for the Homebrew cask in dist/release
```

- `internal/cli`: commands and output
- `internal/detect`: how to start a project
- `internal/launchd`, `internal/proxy`, `internal/bonjour`, `internal/cloudflared`: the machinery
- `internal/config`: the registry and homebase.toml

## License

[MIT](LICENSE)
