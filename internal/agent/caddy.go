package agent

import (
	"bytes"
	"crypto/tls"
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/semenov/ship/internal/proto"
)

const (
	caddyfile    = "/etc/caddy/Caddyfile"
	caddyShipDir = "/etc/caddy/ship"
	caddyImport  = "import " + caddyShipDir + "/*.caddy"
)

func caddyConfPath(app string) string {
	return filepath.Join(caddyShipDir, app+".caddy")
}

func renderCaddySite(app, domain string, port int) []byte {
	return []byte(fmt.Sprintf("# managed by ship: app %s (do not edit, rewritten on every deploy)\n%s {\n\treverse_proxy 127.0.0.1:%d\n}\n", app, domain, port))
}

// ensureCaddyfile makes the main Caddyfile import ship's per-app files. A stock
// Caddyfile (the package's welcome page on :80) is replaced; a custom one gets
// the import line appended.
func ensureCaddyfile() error {
	if err := os.MkdirAll(caddyShipDir, 0o755); err != nil {
		return err
	}
	b, err := os.ReadFile(caddyfile)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	if bytes.Contains(b, []byte(caddyImport)) {
		return nil
	}
	var next []byte
	if len(b) == 0 || bytes.Contains(b, []byte("/usr/share/caddy")) {
		next = []byte("# ship apps (one file per app in " + caddyShipDir + ")\n" + caddyImport + "\n")
	} else {
		next = append(bytes.TrimRight(b, "\n"), []byte("\n\n# ship apps\n"+caddyImport+"\n")...)
	}
	return applyCaddy(caddyfile, next)
}

// applyCaddy writes a config file and reloads Caddy. Caddy loads the new config
// atomically; if it is rejected the old config keeps running and the file is
// restored, so a bad route never affects other sites.
func applyCaddy(path string, content []byte) error {
	prev, prevErr := os.ReadFile(path)
	if prevErr == nil && bytes.Equal(prev, content) {
		return nil
	}
	if err := writeFileAtomic(path, content, 0o644); err != nil {
		return err
	}
	if err := reloadCaddy(); err != nil {
		if prevErr == nil {
			writeFileAtomic(path, prev, 0o644)
		} else {
			os.Remove(path)
		}
		return err
	}
	return nil
}

func reloadCaddy() error {
	out, err := combined("caddy", "reload", "--config", caddyfile, "--adapter", "caddyfile")
	if err != nil {
		return &proto.Error{Code: proto.CodeProxy, Message: "caddy rejected the config; previous config restored", Logs: tail(out, 20)}
	}
	return nil
}

func caddyRoute(cfg *serverConfig, a *proto.App, hostPort int) ([]string, error) {
	if err := ensureCaddyfile(); err != nil {
		return nil, err
	}
	progress("Routing https://%s → 127.0.0.1:%d", a.Domain, hostPort)
	if err := applyCaddy(caddyConfPath(a.Name), renderCaddySite(a.Name, a.Domain, hostPort)); err != nil {
		return nil, err
	}
	a.URL = "https://" + a.Domain
	var warnings []string
	if a.TLS = haveValidTLS(a.Domain, 2*time.Second); !a.TLS {
		progress("Waiting for the TLS certificate")
		a.TLS = waitTLS(a.Domain, 60*time.Second)
	}
	if !a.TLS {
		w := "TLS certificate is not ready yet; Caddy keeps retrying in the background"
		if ok, why := dnsPointsHere(a.Domain, cfg.PublicIP); !ok {
			w += fmt.Sprintf(" (%s; add an A record %s → %s)", why, a.Domain, cfg.PublicIP)
		}
		warnings = append(warnings, w)
	}
	return warnings, nil
}

func waitTLS(domain string, timeout time.Duration) bool {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if haveValidTLS(domain, 3*time.Second) {
			return true
		}
		time.Sleep(2 * time.Second)
	}
	return false
}

// haveValidTLS checks that the local Caddy serves a publicly trusted certificate for domain.
func haveValidTLS(domain string, timeout time.Duration) bool {
	d := &net.Dialer{Timeout: timeout}
	conn, err := tls.DialWithDialer(d, "tcp", "127.0.0.1:443", &tls.Config{ServerName: domain})
	if err != nil {
		return false
	}
	conn.Close()
	return true
}

func caddyUnroute(a *proto.App) error {
	path := caddyConfPath(a.Name)
	if err := os.Remove(path); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		return err
	}
	return reloadCaddy()
}

// removeNginxShipConfs deletes nginx configs written by ship (after switching to Caddy).
func removeNginxShipConfs(dir string) {
	matches, _ := filepath.Glob(filepath.Join(dir, "ship-*.conf"))
	for _, m := range matches {
		if b, err := os.ReadFile(m); err == nil && strings.HasPrefix(string(b), "# managed by ship") {
			os.Remove(m)
		}
	}
}
