# homebase

A CLI for running local dev servers on macOS. Each server is a per-user
launchd agent, and a small reverse proxy makes it available at
`http://<name>.localhost`.

```sh
go install github.com/semenov/homebase@latest   # or `go install .` from a checkout
homebase proxy install                          # one-time: proxy on :80 as a launch agent

cd ~/Dev/my-app
homebase add my-app -- npm run dev -- --port '$PORT'
homebase start my-app
homebase open my-app                            # http://my-app.localhost
homebase ls
homebase logs my-app -f
homebase stop my-app
```

## How it works

- **Config.** Servers are listed in `~/.config/homebase/servers.yaml`
  (`$HOMEBASE_CONFIG` overrides the path). Edit it with `homebase edit` or by hand.
- **Running a server.** Each server becomes `~/Library/LaunchAgents/dev.homebase.server.<name>.plist`.
  It runs `zsh -lc "<command>"` in its directory, so your normal PATH (nvm, brew) works.
  The port is passed in `$PORT`. If you don't pick one, homebase uses the first free port from 4000.
- **Crashes and reboots.** launchd restarts a server if it crashes.
  `start` enables the job and `stop` disables it, so after a reboot or login,
  servers that were running come back and stopped ones stay stopped.
- **Logs.** Output goes to `~/Library/Logs/homebase/<name>.log`.
- **Applying changes.** After changing a server's settings, run `homebase restart <name>`.
  This rewrites the plist and reloads the job.

## Local domains

- **No DNS setup.** macOS resolves `*.localhost` to 127.0.0.1, so you don't need
  `/etc/hosts` or sudo. Subdomains work too: `a.my-app.localhost` goes to `my-app`.
- **Port 80 without root.** macOS lets a normal user bind `:80`, but only on all
  interfaces. Because of that, the proxy rejects any client that isn't on loopback
  unless LAN mode is on.
- **Original Host header.** The proxy keeps the browser's Host header (`my-app.localhost`)
  and supports WebSockets, so HMR works. Vite accepts `*.localhost` hosts by default.
- **Config reload.** The proxy re-reads the config whenever it changes. Adding a server
  doesn't need a proxy restart. Changing `proxy.port` does.
- **Index page.** `http://localhost` lists all servers.

## Other devices on the network (Bonjour)

`homebase lan on` makes servers reachable from your phone or other computers
on the same network at `http://<name>.local`.

- **Announcing names.** The proxy announces each name through the Mac's own
  mDNSResponder, running one `dns-sd -P` process per server.
- **Staying in sync.** Every 5 seconds it updates the names from the config and
  re-announces them if the Mac's IP changes.
- **Which IP.** It announces the Wi-Fi or Ethernet (`en*`) address and never a
  VPN tunnel address.
- **Access control.** Once LAN mode is on, anyone on the network can reach your
  servers. `homebase lan off` withdraws the names and goes back to accepting
  connections from this Mac only.
- **Where it works.** iOS and macOS resolve `.local` names natively. It won't work
  on Wi-Fi networks that isolate clients from each other (many guest and office networks).
- **Plain HTTP.** These are `http://` URLs, so phone browsers turn off features that
  need a secure context: service workers, camera and microphone, and some others.
