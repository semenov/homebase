---
name: homebase
description: Run a project's dev server on this Mac and deploy it to the user's own server with the `homebase` CLI (background launchd service with a stable http://<name>.localhost URL and logs; zero-downtime Docker deploys with HTTPS, Postgres and persistent volumes; sharing dev servers at https://<name>.dev.<domain>). Use when the user asks to run, start, serve, restart or stop a dev server, open the app locally, check why it is not running, show it on a phone or to someone else, or deploy, ship or publish an app (including "deploy to user@host"), add a database, check prod status or logs, roll back, or set production env vars.
---

<!-- managed by `homebase agents`; reinstall with `homebase agents install`, remove with `homebase agents uninstall` -->

# Dev servers and deploys with homebase

`homebase` runs the project in the current folder in two places: its dev server on the
user's Mac (a background service) and the deployed app on the user's own server ("prod").
Use it instead of starting servers yourself (`npm run dev &`) or manual ssh/docker/nginx work.
It never prompts.

0. First run `homebase docs`: the full guide plus a plan for the current folder.

Dev server:

1. Start: `homebase --json` in the project folder. It detects the command, writes
   `homebase.toml`, starts the server and waits until its port is open. Read `data.url`.
   If the stack is not detected, pass `--start '<command listening on $PORT>'` (single quotes).
2. Check: `homebase status --json` (`data.state == "running"` and `data.listening == true`).
   Output: `homebase logs -n 100`. Never `homebase logs -f`, it does not return.
3. On failure stdout is `{"ok":false,"error":{"code","message","hint","logs"}}`:
   - `dependencies_missing`: run the install command from `error.hint`, then `homebase` again.
   - `start_failed`: the command crashed; the cause is in `error.logs`.
   - `not_listening`: it runs but not on `$PORT`; fix `start` in homebase.toml.
   - `port_in_use`: another program holds the port; `homebase --port N` picks another.
4. After changing homebase.toml: `homebase restart --json`.

Deploy:

5. Deploy: `homebase deploy --json`. Read `data.url`. If the error is `config` "no server yet",
   ask the user for their server, then `homebase server add user@host`.
6. Verify: `homebase status --prod --json`, `data.healthy == true`; else `homebase logs --prod -n 100`.
   On `container_failed`/`health_check_failed` the previous release keeps serving: fix the cause
   from `error.logs` and deploy again. Bad release: `homebase rollback --json`.
7. Data: the container is replaced on every deploy. Files/SQLite: `volumes = ["/data"]` under
   [deploy] in homebase.toml, write under `$DATA_DIR`. Postgres: `homebase db add`, read
   `$DATABASE_URL`. Migrations: `release = "npm run migrate"` under [deploy].
8. Secrets: `homebase env set KEY=value --prod` (stored on the server). Never put them in
   homebase.toml, which is committed.

Rules:

- Leave dev servers running unless the user wants them stopped (`homebase stop`).
- Never run `homebase share`, `init`, `uninstall` or `server add` unless the user asks: they
  publish servers on the internet or change the whole machine. Share links with a token are
  secrets; only show them to the user.
- `homebase destroy --yes` removes the deployed app but keeps its data; add `--data` only when
  the user explicitly asks to delete it.

Run `homebase docs` for the full reference (detection, homebase.toml, error codes).
