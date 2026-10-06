package main

import (
	"crypto/rand"
	"encoding/base64"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/semenov/homebase/internal/cloudflared"
	"github.com/semenov/homebase/internal/config"
	"github.com/semenov/homebase/internal/launchd"
	"github.com/semenov/homebase/internal/proxy"
)

const (
	tunnelLabel = "dev.homebase.tunnel"
	tunnelName  = "homebase"
)

func cloudflaredConfigPath() string {
	return filepath.Join(filepath.Dir(config.Path()), "cloudflared.yml")
}

func cmdTunnel(args []string) error {
	if len(args) == 0 {
		return errors.New("usage: homebase tunnel setup <domain> | status | uninstall")
	}
	cfg, err := config.Load()
	if err != nil {
		return err
	}
	switch args[0] {
	case "setup":
		if len(args) != 2 {
			return errors.New("usage: homebase tunnel setup <domain>   (e.g. example.com)")
		}
		return tunnelSetup(cfg, strings.ToLower(strings.TrimSuffix(args[1], ".")))
	case "status":
		if cfg.Tunnel == nil {
			fmt.Println("tunnel: not set up (homebase tunnel setup <domain>)")
			return nil
		}
		st := launchd.Get(tunnelLabel)
		fmt.Printf("tunnel: %s, %q (%s), domain %s\n", state(st), cfg.Tunnel.Name, cfg.Tunnel.ID, cfg.Tunnel.Domain)
		if !launchd.Get(proxyLabel).Running {
			fmt.Println("warning: the proxy is not running — `homebase proxy install`")
		}
		for _, name := range cfg.Names() {
			if sh := cfg.Servers[name].Share; sh != nil {
				fmt.Printf("  %-16s %s (%s)\n", name, cfg.PublicURL(name), shareKind(sh))
			}
		}
		return nil
	case "uninstall":
		if err := launchd.Remove(tunnelLabel); err != nil {
			return err
		}
		if cfg.Tunnel == nil {
			fmt.Println("tunnel was not set up")
			return nil
		}
		name := cfg.Tunnel.Name
		cfg.Tunnel = nil
		if err := cfg.Save(); err != nil {
			return err
		}
		os.Remove(cloudflaredConfigPath())
		restartProxy()
		fmt.Printf("tunnel stopped and removed from homebase. Shares are kept for a future setup.\n"+
			"The tunnel and its DNS records still exist in Cloudflare; to delete the tunnel: cloudflared tunnel delete %s\n", name)
		return nil
	}
	return fmt.Errorf("unknown tunnel command %q", args[0])
}

func tunnelSetup(cfg *config.Config, domain string) error {
	if !strings.Contains(domain, ".") {
		return fmt.Errorf("%q does not look like a domain", domain)
	}
	bin, err := cloudflared.Path()
	if err != nil {
		return err
	}
	if !cloudflared.LoggedIn() {
		return fmt.Errorf("log in to Cloudflare first: run `cloudflared tunnel login` and pick the zone for %s, then rerun this command", domain)
	}
	id, err := cloudflared.EnsureTunnel(tunnelName)
	if err != nil {
		return err
	}
	if _, err := os.Stat(cloudflared.CredentialsPath(id)); err != nil {
		return fmt.Errorf("tunnel %q exists but its credentials are not on this Mac (%s). "+
			"Delete it with `cloudflared tunnel delete %s` and rerun setup", tunnelName, cloudflared.CredentialsPath(id), tunnelName)
	}
	port := config.DefaultTunnelPort
	if cfg.Tunnel != nil && cfg.Tunnel.Port != 0 {
		port = cfg.Tunnel.Port
	}
	cfg.Tunnel = &config.Tunnel{Name: tunnelName, ID: id, Domain: domain, Port: port}
	if err := cloudflared.WriteConfig(cloudflaredConfigPath(), id, port); err != nil {
		return err
	}
	if err := cfg.Save(); err != nil {
		return err
	}
	err = launchd.Start(&launchd.Job{
		Label:     tunnelLabel,
		Args:      []string{bin, "tunnel", "--no-autoupdate", "--config", cloudflaredConfigPath(), "run"},
		LogPath:   logPath("tunnel"),
		KeepAlive: true,
	})
	if err != nil {
		return err
	}
	// The proxy opens its tunnel listener at startup.
	restartProxy()
	fmt.Printf("tunnel %q (%s) is running for *.%s (logs: homebase logs tunnel)\n", tunnelName, id, domain)
	if !launchd.Get(proxyLabel).Loaded {
		fmt.Println("warning: the proxy is not running — `homebase proxy install`")
	}
	fmt.Println("share a server with: homebase share <name>")
	return nil
}

func restartProxy() {
	if launchd.Get(proxyLabel).Loaded {
		if err := launchd.Restart(proxyLabel); err != nil {
			fmt.Fprintln(os.Stderr, "warning: restart proxy:", err)
		}
	}
}

func cmdShare(args []string) error {
	if len(args) == 0 || strings.HasPrefix(args[0], "-") {
		return errors.New("usage: homebase share <name> [--public] [--new-token]")
	}
	name := args[0]
	public, newToken := false, false
	for _, a := range args[1:] {
		switch a {
		case "--public", "-public":
			public = true
		case "--new-token", "-new-token":
			newToken = true
		default:
			return fmt.Errorf("unknown flag %s", a)
		}
	}
	cfg, err := config.Load()
	if err != nil {
		return err
	}
	srv, err := cfg.Get(name)
	if err != nil {
		return err
	}
	if cfg.Tunnel == nil {
		return errors.New("no tunnel yet — run `homebase tunnel setup <domain>` first")
	}
	if !cloudflared.LoggedIn() {
		return errors.New("not logged in to Cloudflare on this Mac — run `cloudflared tunnel login`")
	}
	host := name + "." + cfg.Tunnel.Domain
	if err := cloudflared.RouteDNS(cfg.Tunnel.ID, host); err != nil {
		if strings.Contains(err.Error(), "already exists") {
			return fmt.Errorf("%s already has a DNS record for something else — homebase won't overwrite it. "+
				"Pick another server name or delete the record in the Cloudflare dashboard", host)
		}
		return err
	}

	sh := srv.Share
	if sh == nil {
		sh = &config.Share{}
	}
	sh.Public = public
	if public {
		sh.Token = ""
	} else if sh.Token == "" || newToken {
		if sh.Token, err = randomToken(); err != nil {
			return err
		}
	}
	srv.Share = sh
	if err := cfg.Save(); err != nil {
		return err
	}

	if public {
		fmt.Printf("%s is public: %s\n", name, cfg.PublicURL(name))
	} else {
		fmt.Printf("%s is shared privately. Open this link once per browser:\n  %s\n", name, shareLink(cfg, name))
	}
	if !launchd.Get(tunnelLabel).Running {
		fmt.Println("warning: the tunnel is not running — `homebase tunnel setup " + cfg.Tunnel.Domain + "`")
	}
	if !launchd.Get(serverLabelPrefix + name).Running {
		fmt.Printf("note: %s is not running — homebase start %s\n", name, name)
	}
	fmt.Println("(a new DNS record can take a minute to resolve)")
	return nil
}

func cmdUnshare(args []string) error {
	if len(args) != 1 {
		return errors.New("usage: homebase unshare <name>")
	}
	cfg, err := config.Load()
	if err != nil {
		return err
	}
	srv, err := cfg.Get(args[0])
	if err != nil {
		return err
	}
	srv.Share = nil
	if err := cfg.Save(); err != nil {
		return err
	}
	fmt.Printf("%s is no longer shared", args[0])
	if cfg.Tunnel != nil {
		fmt.Printf("; %s now returns 404.\nIts DNS record stays in Cloudflare (harmless); delete it in the dashboard if you want", cfg.PublicURL(args[0]))
	}
	fmt.Println()
	return nil
}

func shareKind(sh *config.Share) string {
	if sh.Public {
		return "public"
	}
	return "private"
}

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
