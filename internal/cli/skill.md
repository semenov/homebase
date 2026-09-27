---
name: ship
description: Deploy the current web app to the user's own server with the `ship` CLI (build, upload, zero-downtime release behind Caddy with HTTPS, Postgres databases, persistent volumes). Use when the user asks to deploy, ship, publish or put an app online (including "deploy to user@host" or "to my server"), add a database or file storage to a deployed app, check its status or logs, roll back, or set production env vars.
---

<!-- managed by `ship agents`; reinstall with `ship agents install`, remove with `ship agents uninstall` -->

# Deploying with ship

`ship` deploys the project in the current directory to the user's own server over SSH. Use it
instead of manual ssh/docker/nginx work whenever the user asks to deploy, even if they only
name a server ("deploy to root@1.2.3.4"). It never prompts.

0. First run `ship docs`: the full guide plus a plan for the current project (server, build, data).

1. Deploy: `ship --json` (add `--domain x.example.com` for a custom domain, `--port N` if the app
   does not listen on the detected port). Read `data.url` from stdout.
2. Verify: `ship status --json` and check `data.healthy == true`; if not, `ship logs -n 100`.
3. On failure stdout is `{"ok":false,"error":{"code","message","hint","logs"}}`. The previous release
   keeps serving. Fix the cause shown in `error.logs`/`error.hint` and redeploy.
   - `stack_not_detected`: add a Dockerfile or pass `--start "<cmd listening on 0.0.0.0:$PORT>"`.
   - `container_failed` / `health_check_failed`: the app crashed or did not listen on `$PORT`.
   - `release_command_failed`: the migration/release command failed; see `error.logs`.
   - `config` "no server configured": ask the user for the server, then `ship init user@host`.
4. Data: the container is replaced on every deploy, so never rely on its filesystem.
   - Files/uploads/SQLite: add `volumes = ["/data"]` to ship.toml and write under `$DATA_DIR`.
   - Postgres: `ship db add` (works before the first deploy), then read `$DATABASE_URL`.
   - Migrations: `release = "npm run migrate"` in ship.toml; runs before traffic switches.
   - Inspect data: `ship db shell -c "SELECT ..."`. Backup: `ship db backup --download`.
5. Secrets: `ship env set KEY=value` (never commit them, never put them in ship.toml).
6. Roll back a bad release: `ship rollback --json`.
7. `ship destroy --yes` removes the app but keeps its volumes and database. Add `--data` only
   when the user explicitly asks to delete the data.

Run `ship docs` for the full reference (build detection, ship.toml, error codes).
