# ship

Deploy web apps to your own server straight from the terminal. Built to be trivial for
people and for AI agents.

```sh
ship init root@1.2.3.4        # once: remembers the server (--install sets up docker + caddy)
cd my-app && ship             # → https://my-app.1-2-3-4.sslip.io
ship --domain app.example.com # your own domain, automatic HTTPS
```

- **Just SSH.** No registry, no control panel, no daemon. ship installs a tiny helper (`shipd`) and
  keeps it up to date by itself.
- **Caddy for HTTPS.** One small file per app, applied atomically with `caddy reload`; certificates
  are issued once and renewed automatically. Servers that already run nginx are supported too
  (configs validated with `nginx -t`, certificates via certbot).
- **Zero-downtime releases.** The new container has to answer HTTP before traffic switches;
  a broken release never replaces a working one. `ship rollback` switches back instantly.
- **No Dockerfile needed** for Node, Python (Django/FastAPI/Flask), Go and static sites.
- **Agent-friendly.** Never interactive, `--json` everywhere, stable error codes with hints and
  container logs. `ship docs` is a single-page reference; `skills/ship/SKILL.md` is a Claude Code skill.

## Commands

| | |
|---|---|
| `ship [dir]` | build and release (`--domain`, `--port`, `--health`, `--start`, `--remote-build`) |
| `ship status` / `ship ls` | health, URL and releases of one / all apps |
| `ship logs [-f] [-n 100]` | container logs |
| `ship rollback` / `ship restart` | zero-downtime switch to previous / fresh container |
| `ship env ls\|set\|unset` | env vars stored on the server, app restarts automatically |
| `ship destroy --yes` | remove everything belonging to the app |

## Build

```sh
make          # builds linux shipd agents, embeds them, builds dist/ship
make install  # copies ship to ~/.local/bin (override with BINDIR=...)
make test
```

Requires Go and, for local builds, Docker with buildx (images are built for the server's
architecture). Without local Docker, or with `--remote-build`, the image is built on the server.

## Layout

- `cmd/ship`, `internal/cli`: the client
- `cmd/shipd`, `internal/agent`: the server helper (state in `/var/lib/ship`)
- `internal/detect`: stack detection and Dockerfile generation
- `internal/proto`: JSON protocol and error codes shared by both
