---
name: homebase
description: Run a project's local dev server on this Mac with the `homebase` CLI (background launchd service, auto-detected start command, stable http://<name>.localhost URL, logs, LAN and public sharing). Use when the user asks to run, start, serve, restart or stop a dev server, open the app locally, check why it is not running, or show it on a phone or to someone else.
---

<!-- managed by `homebase agents`; reinstall with `homebase agents install`, remove with `homebase agents uninstall` -->

# Running dev servers with homebase

`homebase` runs the dev server of the project in the current folder as a background service
on the user's Mac. Use it instead of starting servers yourself (`npm run dev &`): it keeps
running after your session, restarts on crashes, logs to a file and gets a stable URL.
It never prompts.

0. First run `homebase docs`: the full guide plus a plan for the current folder.

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
   - `stack_not_detected`: pass `--start '...'`.
4. After changing homebase.toml or env: `homebase restart --json`.
5. Leave servers running unless the user wants them stopped (`homebase stop`).
   `homebase forget` unregisters one; project files are never touched.
6. Never run `homebase share`, `init` or `uninstall` unless the user explicitly asks: they
   publish servers on the internet or change the whole machine. Share links with a token are
   secrets; only show them to the user.

Run `homebase docs` for the full reference (detection, homebase.toml, error codes).
