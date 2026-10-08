package agent

import (
	"bufio"
	"bytes"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/semenov/homebase/internal/proto"
)

// A wildcard certificate (*.base-domain, via a DNS challenge) covers every app,
// so deploying a new app needs no certificate request at all: no waiting and
// no Let's Encrypt rate limits. App sites stay separate site blocks; Caddy sees
// that the cached wildcard already covers their names and does not request
// certificates for them (verified with Caddy 2.11, including after restarts).

const (
	dnsEnvFile      = "/etc/caddy/dns.env"
	caddyDropIn     = "/etc/systemd/system/caddy.service.d/ship-dns.conf"
	wildcardConf    = caddyShipDir + "/_wildcard.caddy" // app names cannot start with "_"
	cloudflareMod   = "dns.providers.cloudflare"
	cloudflarePkg   = "github.com/caddy-dns/cloudflare"
	cloudflareToken = "CLOUDFLARE_API_TOKEN"
)

// setupWildcard configures Caddy to obtain *.base via Cloudflare DNS. The API
// token is read from stdin so it never appears in process arguments.
func setupWildcard(cfg *serverConfig, provider string, stdin io.Reader) error {
	if provider != "cloudflare" {
		return proto.Errf(proto.CodeUsage, "supported: cloudflare", "unsupported DNS provider %q", provider)
	}
	if cfg.proxy() != proxyCaddy {
		return proto.Errf(proto.CodeConfig, "", "wildcard certificates need caddy as the proxy (this server uses %s)", cfg.proxy())
	}
	if cfg.BaseDomain == "" {
		return proto.Errf(proto.CodeConfig, "pass --base-domain example.com (with a wildcard DNS record *.example.com → this server)", "wildcard certificates need a base domain")
	}
	token, _ := bufio.NewReader(stdin).ReadString('\n')
	if token = strings.TrimSpace(token); token == "" {
		return proto.Errf(proto.CodeConfig, "set CLOUDFLARE_API_TOKEN or pass --dns-token-file", "no DNS API token given")
	}

	if err := ensureCaddyModule(cloudflareMod, cloudflarePkg); err != nil {
		return err
	}
	env := []byte(cloudflareToken + "=" + token + "\n")
	old, _ := os.ReadFile(dnsEnvFile)
	if err := os.WriteFile(dnsEnvFile, env, 0o600); err != nil {
		return err
	}
	dropIn := []byte("# managed by ship: DNS API token for wildcard certificates\n[Service]\nEnvironmentFile=" + dnsEnvFile + "\n")
	prevDropIn, _ := os.ReadFile(caddyDropIn)
	if !bytes.Equal(old, env) || !bytes.Equal(prevDropIn, dropIn) {
		if err := os.MkdirAll(filepath.Dir(caddyDropIn), 0o755); err != nil {
			return err
		}
		if err := os.WriteFile(caddyDropIn, dropIn, 0o644); err != nil {
			return err
		}
		progress("Restarting caddy to load the DNS token")
		combined("systemctl", "daemon-reload")
		if out, err := combined("systemctl", "restart", "caddy"); err != nil {
			return &proto.Error{Code: proto.CodeProxy, Message: "caddy failed to restart", Logs: tail(out, 10)}
		}
		waitCaddyAdmin()
	}

	site := fmt.Sprintf("# managed by ship: one certificate for all apps (DNS challenge via %s)\n*.%s {\n\ttls {\n\t\tdns %s {env.%s}\n\t}\n\trespond \"Not found\" 404\n}\n",
		provider, cfg.BaseDomain, provider, cloudflareToken)
	if err := applyCaddy(wildcardConf, []byte(site)); err != nil {
		return err
	}
	cfg.WildcardDNS = provider

	probe := "ship-wildcard-check." + cfg.BaseDomain
	progress("Waiting for the wildcard certificate *.%s (DNS challenge, up to 3 minutes)", cfg.BaseDomain)
	if !waitTLS(probe, 3*time.Minute) {
		out, _ := combined("journalctl", "-u", "caddy", "--since", "-5min", "--no-pager")
		return &proto.Error{Code: proto.CodeProxy, Message: "wildcard certificate was not issued in time; Caddy keeps retrying",
			Hint: "check that the token can edit DNS records of the zone", Logs: grepLines(out, cfg.BaseDomain, 15)}
	}
	return nil
}

func ensureCaddyModule(module, pkg string) error {
	if out, _ := output("caddy", "list-modules"); strings.Contains(out, module) {
		return nil
	}
	progress("Adding %s to caddy (downloads a custom build)", pkg)
	if out, err := combined("caddy", "add-package", pkg); err != nil {
		return &proto.Error{Code: proto.CodeServerNotReady, Message: "caddy add-package failed", Logs: tail(out, 15)}
	}
	// keep apt from replacing the custom build with a stock one
	if _, err := exec.LookPath("apt-mark"); err == nil {
		combined("apt-mark", "hold", "caddy")
	}
	progress("Restarting caddy with the new build")
	if out, err := combined("systemctl", "restart", "caddy"); err != nil {
		return &proto.Error{Code: proto.CodeProxy, Message: "caddy failed to restart", Logs: tail(out, 10)}
	}
	waitCaddyAdmin()
	return nil
}

func waitCaddyAdmin() {
	for i := 0; i < 30; i++ {
		if exec.Command("curl", "-sf", "-o", "/dev/null", "http://localhost:2019/config/").Run() == nil {
			return
		}
		time.Sleep(time.Second)
	}
}

func grepLines(s, substr string, n int) string {
	var out []string
	for _, l := range strings.Split(s, "\n") {
		if strings.Contains(l, substr) {
			out = append(out, l)
		}
	}
	return tail(strings.Join(out, "\n"), n)
}
