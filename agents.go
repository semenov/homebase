package main

// agentGuide is printed by `homebase agents`: everything a coding agent
// needs to run a project's dev server without reading the source.
const agentGuide = `# homebase: guide for AI agents

homebase runs long-lived local dev servers on macOS as launchd agents.
Use it instead of backgrounding processes yourself (` + "`npm run dev &`" + `):
servers outlive your session, restart after a crash, log to a file, and get
stable URLs (http://<name>.localhost).

## Workflow

1. Register the server (safe to repeat: -force overwrites):

     homebase add <name> -dir <project-dir> -force -- '<command>'

   The command must listen on $PORT. homebase picks a free port (4000+) and
   passes it in the environment; -port N pins one. Quote the command in
   single quotes so $PORT is expanded by the server's shell, not yours.
   If the tool ignores $PORT, pass it explicitly:

     -- 'npx vite --port $PORT --strictPort'
     -- 'npx next dev -p $PORT'
     -- 'python3 -m http.server $PORT'
     -- 'uvicorn app:app --reload --port $PORT'

2. Start it and wait until it accepts connections:

     homebase start <name> --wait [--timeout 120s]

   Exit code 0 means the port is open. Non-zero means it crashed, exited,
   or timed out; the log output since the start is printed to stderr.

3. Use it: http://localhost:<port> always works. http://<name>.localhost
   needs the proxy (` + "`proxy.running`" + ` in ` + "`homebase ls --json`" + `).

4. Inspect:

     homebase ls --json          state, pid, port, listening, urls, log path
     homebase logs <name> -n 100 last lines of the log (stdout + stderr)

5. After changing the command, dir, env or port: run ` + "`homebase add ... -force`" + `,
   then ` + "`homebase restart <name> --wait`" + `. Config edits don't apply to a
   running server until restart.

6. When done: leave it running if the user wants it, otherwise
   ` + "`homebase stop <name>`" + `. ` + "`homebase rm <name>`" + ` unregisters it.

## Rules

- Never run ` + "`homebase logs -f`" + ` (follows forever) or ` + "`homebase edit`" + ` (opens an
  editor). Use ` + "`logs -n N`" + ` or read the log file directly.
- Check ` + "`homebase ls --json`" + ` before adding: the server may already exist under
  another name. Reuse it rather than creating duplicates.
- Names are lowercase letters, digits and dashes; they become hostnames.
- Servers keep running after your session ends and come back after a reboot
  (until stopped).
- Ask the user before ` + "`homebase proxy install|uninstall`" + ` or ` + "`homebase lan on|off`" + `:
  they affect the whole machine (port 80) and the local network.
- Extra environment: ` + "`-env KEY=VALUE`" + ` (repeatable) on ` + "`add`" + `. The command runs via
  ` + "`zsh -lc`" + ` in -dir, so the user's PATH (nvm, brew) is available.

## Reference

  homebase ls --json fields per server:
    name, state (running|stopped|crashed|exited), last_exit, pid, port,
    listening, url, local_url, lan_url (when LAN mode is on), dir, command,
    env, log
  Config:   ~/.config/homebase/servers.yaml ($HOMEBASE_CONFIG overrides)
  Logs:     ~/Library/Logs/homebase/<name>.log
  Exit codes: 0 success, 1 error, 2 usage
`
