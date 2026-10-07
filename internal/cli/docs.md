# homebase: the full guide

homebase runs local dev servers on macOS. Each server is a launchd agent: it runs in the
background, survives the end of your terminal or agent session, restarts after a crash,
comes back after a reboot (until stopped) and writes its output to a log file. A small
proxy gives every server a stable URL, http://<name>.localhost.

## Starting a project

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
--timeout 2m, -a NAME (server name; default: the folder name).

## Detection

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

## homebase.toml

    name = "my-app"                       # server name and hostname (my-app.localhost)
    start = "pnpm dev --port $PORT"       # runs via zsh -lc in this folder
    port = 3000                           # optional: pin a port (normally picked per machine)

    [env]
    API_URL = "http://api.localhost"      # extra environment (not for secrets)

## Commands

    homebase [dir]          start the project (see above); `homebase start` is the same
    homebase status         this project's server: state, URLs, port, pid, log path
    homebase ls             all servers on this Mac, plus the machine setup
    homebase logs [-n 50]   the server's output; -f follows (never returns)
    homebase open           open it in the browser
    homebase restart        restart, re-reading homebase.toml (--all for every server)
    homebase stop           stop it; it stays stopped after a reboot (--all)
    homebase forget         stop it and remove it from homebase; files are not touched
    homebase share          publish at https://<name>.<domain> (--private for a token)
    homebase unshare        stop publishing
    homebase init           set up this Mac (proxy; --lan, --tunnel DOMAIN)
    homebase uninstall      stop everything homebase runs; settings are kept
    homebase agents install tell coding agents on this Mac about homebase
    homebase docs           this guide plus a plan for the current folder

Commands act on the server of the current folder. From elsewhere pass -a NAME.
Every command takes --json.

## URLs

    http://localhost:<port>          always works
    http://<name>.localhost          through the proxy (`homebase init`)
    http://<name>.local              other devices on the network (`homebase init --lan`)
    https://<name>.<domain>          from anywhere, for shared servers
                                     (`homebase init --tunnel DOMAIN`, then `homebase share`)

Through the proxy the server sees the original Host header. Through the tunnel it sees
Host <name>.localhost (so dev servers' host checks pass); the public host is in
X-Forwarded-Host, with X-Forwarded-Proto https.

## JSON output

With --json stdout is exactly one object:

    {"ok": true, "data": {...}}
    {"ok": false, "error": {"code": "...", "message": "...", "hint": "...", "logs": "..."}}

Server objects (homebase, status, share) have: name, state (running|stopped|crashed|
exited), last_exit, pid, uptime, port, listening, url, local_url, lan_url, public_url,
shared (public|private), share_link, dir, command, env, log.
`homebase ls` returns {"servers": [...], "machine": {...}}.

## Errors

    code                  exit  meaning
    usage                 2     wrong flags or arguments
    config                3     settings or homebase.toml problem
    stack_not_detected    3     pass --start '<cmd>'
    dependencies_missing  3     run the install command in the hint
    port_in_use           5     another program holds the port: --port N
    start_failed          5     the command exited; see logs
    not_listening         5     it runs, but not on $PORT
    launchd_failed        5     macOS refused to start the agent
    cloudflare_failed     6     tunnel or DNS problem
    server_not_found      7     no server here: run `homebase`, or pass -a NAME
    internal              1     anything else

## Rules for agents

- Prefer `homebase --json` over starting servers yourself.
- Never run `homebase logs -f`; use `homebase logs -n 100`.
- Ask before `homebase share`, `init` and `uninstall`: they publish servers on the internet
  or change the whole machine. Share links contain a secret token.
- Leave the server running when you are done unless the user wants it stopped.

## Files

    homebase.toml                         per project, meant to be committed
    ~/.config/homebase/servers.yaml       registry: folders, ports, shares (0600)
    ~/Library/LaunchAgents/dev.homebase.* launch agents
    ~/Library/Logs/homebase/<name>.log    logs (also proxy.log, tunnel.log)
