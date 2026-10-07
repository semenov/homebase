package cli

import (
	"crypto/rand"
	"encoding/base64"
	"net"
	"strings"

	"github.com/spf13/cobra"

	"github.com/semenov/homebase/internal/cloudflared"
	"github.com/semenov/homebase/internal/config"
	"github.com/semenov/homebase/internal/launchd"
	"github.com/semenov/homebase/internal/proxy"
)

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

func shareCmd() *cobra.Command {
	var private, public, newToken bool
	c := &cobra.Command{
		Use:   "share",
		Short: "Publish the server at https://<name>.<your-domain> (needs `homebase init --tunnel`)",
		Long: `Publishes the server through the Cloudflare Tunnel at https://<name>.<domain>.

A shared server is public. With --private it only opens through a link with a
secret token (it sets a cookie on first visit) or with the X-Homebase-Token
header. Running share again without flags keeps the current mode.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			if private && public {
				return errf(CodeUsage, "", "--private and --public contradict each other")
			}
			if public && newToken {
				return errf(CodeUsage, "", "--new-token only applies to private shares")
			}
			cfg, err := loadConfig()
			if err != nil {
				return err
			}
			name, srv, err := target(cfg)
			if err != nil {
				return err
			}
			if cfg.Tunnel == nil {
				return errf(CodeConfig, "set it up once: homebase init --tunnel example.com (a domain on Cloudflare)", "there is no tunnel yet")
			}
			if !cloudflared.LoggedIn() {
				return errf(CodeCloudflare, "run `cloudflared tunnel login` and pick your domain", "this Mac is not logged in to Cloudflare")
			}
			host := name + "." + cfg.Tunnel.Domain
			// A wildcard record (*.domain) doesn't block the new record, which
			// then silently takes the name over from whatever it pointed to.
			var before []string
			if srv.Share == nil {
				before, _ = net.LookupHost(host)
			}
			step("Pointing %s at the tunnel", host)
			if err := cloudflared.RouteDNS(cfg.Tunnel.ID, host); err != nil {
				if strings.Contains(err.Error(), "already exists") {
					return errf(CodeCloudflare, "pick another name (homebase -a NAME) or delete the record in the Cloudflare dashboard",
						"%s already has a DNS record for something else; homebase won't overwrite it", host)
				}
				return errf(CodeCloudflare, "", "%v", err)
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

			in := info(cfg, name)
			emit(in, func(u *UI) {
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
				if len(before) > 0 {
					u.Warn("%s used to point at %s", host, strings.Join(before, ", "))
					u.Para("Probably a wildcard record. This name now goes to your Mac instead, and `homebase unshare` won't give it back: delete the record in Cloudflare for that.")
				}
				if !launchd.Get(tunnelLabel).Running {
					u.Warn("The tunnel is not running")
					u.Hint("Start it with", "homebase init")
				}
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
		Short: "Stop publishing the server; its public URL returns 404",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, err := loadConfig()
			if err != nil {
				return err
			}
			name, srv, err := target(cfg)
			if err != nil {
				return err
			}
			wasShared := srv.Share != nil
			srv.Share = nil
			if err := saveConfig(cfg); err != nil {
				return err
			}
			emit(map[string]any{"name": name, "was_shared": wasShared}, func(u *UI) {
				if !wasShared {
					u.Off("%s was not shared", name)
					return
				}
				u.Off("%s is no longer shared", name)
				if cfg.Tunnel != nil {
					u.Para("%s now returns 404. Its DNS record stays in Cloudflare (harmless); delete it in the dashboard if you like.", cfg.PublicURL(name))
				}
			})
			return nil
		},
	}
}
