package cli

import (
	"crypto/rand"
	"crypto/tls"
	"encoding/base64"
	"encoding/hex"
	"log"
	"net/http"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/spf13/cobra"

	"github.com/semenov/homebase/internal/config"
	"github.com/semenov/homebase/internal/launchd"
	"github.com/semenov/homebase/internal/proto"
	"github.com/semenov/homebase/internal/proxy"
)

// Sharing publishes a dev server at https://<name>.<dev domain> through the
// user's server: the Mac keeps a reverse SSH tunnel open to it (a launchd
// agent running `homebase tunnel run`), and the server's proxy routes the
// name into the tunnel, to the proxy's share port on this Mac. DNS is one
// wildcard record, so sharing never changes DNS.

// shareLink is the URL to open a server, including the token if private.
func shareLink(cfg *config.Config, name string) string {
	u := cfg.PublicURL(name)
	if sh := cfg.Servers[name].Share; sh != nil && !sh.Public {
		u += "/?" + proxy.TokenParam + "=" + sh.Token
	}
	return u
}

func randomToken() (string, error) {
	b := make([]byte, 24)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(b), nil
}

func sharedNames(cfg *config.Config) []string {
	var names []string
	for _, n := range cfg.Names() {
		if cfg.Servers[n].Share != nil {
			names = append(names, n)
		}
	}
	return names
}

// macName is this Mac's name for people, e.g. "Vlad's MacBook Pro".
func macName() string {
	if out, err := exec.Command("scutil", "--get", "ComputerName").Output(); err == nil && len(out) > 1 {
		return strings.TrimSpace(string(out))
	}
	h, _ := os.Hostname()
	return strings.TrimSuffix(h, ".local")
}

// ensureMacID gives this Mac a stable id on the server; it owns its shares.
func ensureMacID(cfg *config.Config) {
	if cfg.Remote.MacID != "" {
		return
	}
	b := make([]byte, 3)
	rand.Read(b)
	base := strings.Trim(nonNameRe.ReplaceAllString(strings.ToLower(macName()), "-"), "-")
	if len(base) > 40 {
		base = strings.TrimRight(base[:40], "-")
	}
	if base == "" {
		base = "mac"
	}
	cfg.Remote.MacID = base + "-" + hex.EncodeToString(b)
}

// requireRemote is the server shares go through.
func requireRemote(cfg *config.Config) error {
	if cfg.Remote == nil {
		return errf(CodeConfig, "add your server once: homebase server add user@host (see `homebase server add --help`)",
			"there is no server to share through yet")
	}
	ensureMacID(cfg)
	return nil
}

// publish routes the server's public name to this Mac.
func publish(r *Remote, cfg *config.Config, name string) (*proto.Share, error) {
	var t proto.Tunnel
	if err := r.Agent(nil, &t, "tunnel", "--mac", cfg.Remote.MacID, "--mac-name", macName()); err != nil {
		return nil, err
	}
	cfg.Remote.DevDomain = t.DevDomain
	var sh proto.Share
	if err := r.Agent(nil, &sh, "share", "add", "--name", name, "--mac", cfg.Remote.MacID, "--mac-name", macName()); err != nil {
		return nil, err
	}
	return &sh, nil
}

// unpublish removes the route on the server. It is best effort: the proxy on
// this Mac already answers 404 for servers that are not shared.
func unpublish(cfg *config.Config, name string) error {
	if cfg.Remote == nil || cfg.Remote.MacID == "" {
		return nil
	}
	r, err := Connect(cfg.Remote.Host)
	if err != nil {
		return err
	}
	return r.Agent(nil, nil, "share", "rm", "--name", name, "--mac", cfg.Remote.MacID)
}

// ensureTunnel starts the tunnel agent if it doesn't run, or restarts it.
func ensureTunnel(restart bool) error {
	if launchd.Get(tunnelLabel).Running && !restart {
		return nil
	}
	exe, err := stableExecutable()
	if err != nil {
		return err
	}
	step("Starting the tunnel to the server")
	job := &launchd.Job{
		Label:     tunnelLabel,
		Args:      []string{exe, "tunnel", "run"},
		LogPath:   logPath("tunnel"),
		KeepAlive: true,
	}
	if p := os.Getenv("HOMEBASE_CONFIG"); p != "" {
		job.Env = map[string]string{"HOMEBASE_CONFIG": p}
	}
	if err := launchd.Start(job); err != nil {
		return errf(CodeLaunchd, "", "start the tunnel: %v", err)
	}
	return nil
}

// stopTunnelIfUnused removes the tunnel agent once nothing is shared.
func stopTunnelIfUnused(cfg *config.Config) {
	if len(sharedNames(cfg)) == 0 {
		launchd.Remove(tunnelLabel)
	}
}

// reachable waits until url answers through the tunnel. The server's proxy
// answers 502 while the Mac is not connected.
func reachable(url string, timeout time.Duration) bool {
	client := &http.Client{
		Timeout:       5 * time.Second,
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
		Transport:     &http.Transport{TLSClientConfig: &tls.Config{}},
	}
	for deadline := time.Now().Add(timeout); time.Now().Before(deadline); time.Sleep(time.Second) {
		resp, err := client.Get(url)
		if err != nil {
			continue
		}
		resp.Body.Close()
		if resp.StatusCode != http.StatusBadGateway {
			return true
		}
	}
	return false
}

func shareCmd() *cobra.Command {
	var private, public, newToken bool
	c := &cobra.Command{
		Use:   "share",
		Short: "Publish the dev server at https://<name>.dev.<your-domain>, through your server",
		Long: `Publishes the dev server on this Mac at https://<name>.<dev domain> through your server
(` + "`homebase server add`" + `): the Mac keeps an SSH tunnel open to it, and the server's proxy
sends the name into the tunnel. Nothing changes in DNS.

A shared server is public. With --private it only opens through a link with a secret
token (it sets a cookie on first visit) or with the X-Homebase-Token header. Running
share again without flags keeps the current mode.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			if private && public {
				return errf(CodeUsage, "", "--private and --public contradict each other")
			}
			if public && newToken {
				return errf(CodeUsage, "", "--new-token only applies to private shares")
			}
			if err := macOnly(); err != nil {
				return err
			}
			cfg, err := loadConfig()
			if err != nil {
				return err
			}
			name, srv, err := target(cfg)
			if err != nil {
				return err
			}
			if err := requireRemote(cfg); err != nil {
				return err
			}
			legacy := dropLegacyTunnel(cfg)
			step("Connecting this Mac to %s", cfg.Remote.Host)
			r, err := Connect(cfg.Remote.Host)
			if err != nil {
				return err
			}
			res, err := publish(r, cfg, name)
			if err != nil {
				return err
			}
			if legacy != nil {
				// the others were shared through the Cloudflare tunnel too
				for _, n := range sharedNames(cfg) {
					if n != name {
						if _, err := publish(r, cfg, n); err != nil {
							return err
						}
					}
				}
			}

			sh := srv.Share
			if sh == nil {
				sh = &config.Share{Public: true}
			}
			switch {
			case public:
				sh.Public = true
			case private || newToken:
				sh.Public = false
			}
			if sh.Public {
				sh.Token = ""
			} else if sh.Token == "" || newToken {
				if sh.Token, err = randomToken(); err != nil {
					return err
				}
			}
			srv.Share = sh
			if err := saveConfig(cfg); err != nil {
				return err
			}
			if err := ensureTunnel(false); err != nil {
				return err
			}
			in := info(cfg, name)
			connected := true
			if res.TLS && in.State == "running" {
				sp := spin("Waiting for " + res.Host + " to answer")
				connected = reachable(res.URL, 20*time.Second)
				sp.Stop()
			}

			out := struct {
				serverInfo
				Warnings []string `json:"warnings,omitempty"`
			}{in, res.Warnings}
			emit(out, func(u *UI) {
				if sh.Public {
					u.OK("%s is public", name)
					u.KV("URL", in.PublicURL)
					u.Para("Anyone with the URL can open it. `homebase share --private` requires a token instead.")
				} else {
					u.OK("%s is shared privately", name)
					u.KV("Link", in.ShareLink)
					u.Para("Open the link once per browser; it sets a cookie and then the plain URL works. " +
						"Apps and scripts can send the token in an X-Homebase-Token header. Treat the link like a password.")
				}
				for _, w := range res.Warnings {
					u.Bullet(u.p.amber("!"), w)
				}
				if !connected {
					u.Warn("%s doesn't answer through the tunnel yet", res.Host)
					u.Hint("See what the tunnel is doing with", "homebase logs tunnel")
				}
				legacyNotice(u, legacy)
				if in.State != "running" {
					u.Hint(name+" is not running. Start it with", "homebase")
				}
			})
			return nil
		},
	}
	c.Flags().BoolVar(&private, "private", false, "require a secret token")
	c.Flags().BoolVar(&public, "public", false, "make a private share public again")
	c.Flags().BoolVar(&newToken, "new-token", false, "replace the token; old links stop working")
	return c
}

func unshareCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "unshare",
		Short: "Stop publishing the dev server",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := macOnly(); err != nil {
				return err
			}
			cfg, err := loadConfig()
			if err != nil {
				return err
			}
			name, srv, err := target(cfg)
			if err != nil {
				return err
			}
			wasShared := srv.Share != nil
			url := cfg.PublicURL(name)
			srv.Share = nil
			if err := saveConfig(cfg); err != nil {
				return err
			}
			var remoteErr error
			if wasShared {
				remoteErr = unpublish(cfg, name)
				stopTunnelIfUnused(cfg)
			}
			emit(map[string]any{"name": name, "was_shared": wasShared}, func(u *UI) {
				if !wasShared {
					u.Off("%s was not shared", name)
					return
				}
				u.Off("%s is no longer shared", name)
				if remoteErr != nil {
					u.Para("Couldn't remove its route from the server (%v), so %s now answers 404 from this Mac. "+
						"Run `homebase unshare` again later to remove it.", remoteErr, url)
				}
			})
			return nil
		},
	}
}

// ---- migration from the Cloudflare tunnel ---------------------------------

type legacyShares struct {
	Domain string   `json:"domain"`
	Names  []string `json:"names"` // their DNS records still point at the old tunnel
}

// dropLegacyTunnel removes the Cloudflare tunnel of older versions. The DNS
// records it created stay in Cloudflare (cloudflared can't delete them) and
// override a wildcard record, so the user has to delete them.
func dropLegacyTunnel(cfg *config.Config) *legacyShares {
	if cfg.Tunnel == nil {
		return nil
	}
	step("Removing the Cloudflare tunnel (shares now go through your server)")
	launchd.Remove(tunnelLabel)
	os.Remove(filepath.Join(filepath.Dir(config.Path()), "cloudflared.yml"))
	l := &legacyShares{Domain: cfg.Tunnel.Domain, Names: sharedNames(cfg)}
	cfg.Tunnel = nil
	return l
}

func legacyNotice(u *UI, l *legacyShares) {
	if l == nil || len(l.Names) == 0 {
		return
	}
	var hosts []string
	for _, n := range l.Names {
		hosts = append(hosts, n+"."+l.Domain)
	}
	u.Warn("Delete the old tunnel's DNS records in Cloudflare")
	u.Para("homebase no longer uses the Cloudflare tunnel. These records still point at it and "+
		"override a wildcard record for your server: %s.", strings.Join(hosts, ", "))
}

// ---- the tunnel agent -----------------------------------------------------

// tunnelCmd is what the tunnel launch agent runs; not for people.
func tunnelCmd() *cobra.Command {
	c := &cobra.Command{Use: "tunnel", Hidden: true}
	c.AddCommand(&cobra.Command{
		Use:  "run",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error { runTunnel(); return nil },
	})
	return c
}

// tunnelSSH is the running ssh, stopped together with the tunnel agent.
var tunnelSSH atomic.Pointer[exec.Cmd]

// runTunnel keeps a reverse SSH tunnel from the server to the proxy's share
// port open, reconnecting with backoff. It never returns.
func runTunnel() {
	log.SetFlags(log.LstdFlags)
	sig := make(chan os.Signal, 1)
	signal.Notify(sig, syscall.SIGTERM, syscall.SIGINT)
	go func() {
		<-sig
		if c := tunnelSSH.Load(); c != nil && c.Process != nil {
			c.Process.Kill()
		}
		os.Exit(0)
	}()
	backoff := 2 * time.Second
	for {
		started := time.Now()
		if err := tunnelOnce(); err != nil {
			log.Printf("%v", err)
		}
		if time.Since(started) > time.Minute {
			backoff = 2 * time.Second
		}
		log.Printf("reconnecting in %s", backoff)
		time.Sleep(backoff)
		backoff = min(backoff*2, time.Minute)
	}
}

func tunnelOnce() error {
	cfg, err := config.Load()
	if err != nil {
		return err
	}
	if cfg.Remote == nil || cfg.Remote.MacID == "" {
		return errf(CodeConfig, "", "no server configured")
	}
	host := cfg.Remote.Host
	r, err := Connect(host)
	if err != nil {
		return describe(err)
	}
	var t proto.Tunnel
	// --reconnect closes the session of a previous connection that may
	// still hold the port.
	if err := r.Agent(nil, &t, "tunnel", "--mac", cfg.Remote.MacID, "--mac-name", macName(), "--reconnect"); err != nil {
		return describe(err)
	}
	log.Printf("connecting to %s: server port %d → this Mac's port %d", host, t.Port, cfg.Proxy.SharePort)
	cmd := exec.Command("ssh", "-NT",
		"-o", "BatchMode=yes",
		"-o", "ConnectTimeout=15",
		"-o", "ServerAliveInterval=15",
		"-o", "ServerAliveCountMax=3",
		"-o", "ExitOnForwardFailure=yes",
		"-o", "ControlMaster=no",
		"-o", "ControlPath=none",
		"-R", "127.0.0.1:"+strconv.Itoa(t.Port)+":127.0.0.1:"+strconv.Itoa(cfg.Proxy.SharePort),
		host)
	cmd.Stdout, cmd.Stderr = os.Stdout, os.Stderr
	if err := cmd.Start(); err != nil {
		return err
	}
	tunnelSSH.Store(cmd)
	err = cmd.Wait()
	tunnelSSH.Store(nil)
	log.Printf("tunnel closed: %v", err)
	return nil
}

func describe(err error) error {
	if e, ok := err.(*proto.Error); ok && e.Logs != "" {
		return errf(e.Code, "", "%s: %s", e.Message, e.Logs)
	}
	return err
}
