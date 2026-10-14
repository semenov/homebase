package agent

import (
	"bytes"
	"crypto/tls"
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/semenov/homebase/internal/proto"
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
	return []byte(fmt.Sprintf("# managed by homebase: app %s (do not edit, rewritten on every deploy)\n%s {\n%s}\n",
		app, domain, reverseProxy(port)))
}

// streamCloseDelay keeps WebSockets and other streams open across a config
// reload. Every deploy reloads Caddy, which by default closes all upgraded
// connections of the old config, e.g. every app's WebSockets. Caddy still
// closes them once the delay is over, so it is long: connections end on their
// own (or when their app is redeployed, which stops the old container).
const streamCloseDelay = "24h"

// reverseProxy is the proxy directive of every site homebase writes.
func reverseProxy(port int) string {
	if !caddyAtLeast(2, 7) { // stream_close_delay is new in Caddy 2.7
		return fmt.Sprintf("\treverse_proxy 127.0.0.1:%d\n", port)
	}
	return fmt.Sprintf("\treverse_proxy 127.0.0.1:%d {\n\t\tstream_close_delay %s\n\t}\n", port, streamCloseDelay)
}

var (
	caddyVersionOnce sync.Once
	caddyMajor       int
	caddyMinor       int
	caddyVersionRe   = regexp.MustCompile(`^v?(\d+)\.(\d+)`)
)

func caddyAtLeast(major, minor int) bool {
	caddyVersionOnce.Do(func() {
		out, _ := output("caddy", "version")
		if m := caddyVersionRe.FindStringSubmatch(strings.TrimSpace(out)); m != nil {
			caddyMajor, _ = strconv.Atoi(m[1])
			caddyMinor, _ = strconv.Atoi(m[2])
		}
	})
	return caddyMajor > major || caddyMajor == major && caddyMinor >= minor
}

// refreshSites rewrites the files of the other apps and the shares when the
// way homebase renders them changed (e.g. after an update), so that every
// site gets the current settings with the next reload. It returns the
// previous contents, to restore them if Caddy rejects the config.
func refreshSites(skip string) map[string][]byte {
	want := map[string][]byte{}
	apps, _ := listApps()
	for _, a := range apps {
		if a.Current != nil && a.Domain != "" {
			want[caddyConfPath(a.Name)] = renderCaddySite(a.Name, a.Domain, a.Current.HostPort)
		}
	}
	shares, _ := listShares()
	tunnels, _ := readTunnels()
	for _, sh := range shares {
		if t := tunnels[sh.Mac]; t != nil {
			want[caddyConfPath(shareKey(sh.Name))] = renderShareSite(sh, t.Port)
		}
	}
	prev := map[string][]byte{}
	for path, content := range want {
		old, err := os.ReadFile(path)
		if path == skip || err != nil || bytes.Equal(old, content) {
			continue // only sites that are routed now
		}
		if writeFileAtomic(path, content, 0o644) == nil {
			prev[path] = old
		}
	}
	return prev
}

// syncSites applies refreshSites with one reload.
func syncSites() error {
	prev := refreshSites("")
	if len(prev) == 0 {
		return nil
	}
	progress("Updating the proxy config of %d sites", len(prev))
	if err := reloadCaddy(); err != nil {
		restore(prev)
		return err
	}
	return nil
}

func restore(prev map[string][]byte) {
	for path, b := range prev {
		writeFileAtomic(path, b, 0o644)
	}
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

// applySites is applyCaddy that also brings the other sites up to date in the
// same reload.
func applySites(path string, content []byte) error {
	prev := refreshSites(path)
	cur, err := os.ReadFile(path)
	if err == nil && bytes.Equal(cur, content) {
		if len(prev) == 0 {
			return nil
		}
		if err := reloadCaddy(); err != nil {
			restore(prev)
			return err
		}
		return nil
	}
	if err := applyCaddy(path, content); err != nil {
		restore(prev)
		if len(prev) > 0 {
			reloadCaddy()
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
	if err := applySites(caddyConfPath(a.Name), renderCaddySite(a.Name, a.Domain, hostPort)); err != nil {
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
