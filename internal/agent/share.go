package agent

import (
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/semenov/homebase/internal/proto"
)

// Shares publish dev servers running on a Mac. The Mac keeps a reverse SSH
// tunnel open to 127.0.0.1:<port> here (one port per Mac), and the proxy
// routes https://<name>.<dev domain> into it. DNS is a wildcard record set up
// once, so sharing never touches DNS.

const (
	sharesDir       = stateDir + "/shares"
	tunnelsFile     = stateDir + "/tunnels.json"
	tunnelPortStart = 30000
	tunnelPortEnd   = 30999
)

var (
	macIDRe  = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,62}$`)
	ssPIDRe  = regexp.MustCompile(`pid=(\d+)`)
	unsafeRe = regexp.MustCompile(`[\x00-\x1f"{}\\]+`) // would break the Caddyfile
)

// devDomain is where shares go: the configured one, dev.<base domain>, or
// dev.<ip>.sslip.io.
func devDomain(cfg *serverConfig) (string, error) {
	if d := devDomainOrEmpty(cfg); d != "" {
		return d, nil
	}
	return "", proto.Errf(proto.CodeConfig, "set one with `homebase server add <host> --domain example.com`", "cannot pick a domain for shares: the server's public IP is unknown")
}

func devDomainOrEmpty(cfg *serverConfig) string {
	switch {
	case cfg.DevDomain != "":
		return cfg.DevDomain
	case cfg.BaseDomain != "":
		return "dev." + cfg.BaseDomain
	}
	if ip := net.ParseIP(cfg.PublicIP); ip != nil && ip.To4() != nil {
		return "dev." + strings.ReplaceAll(cfg.PublicIP, ".", "-") + ".sslip.io"
	}
	return ""
}

type tunnelEntry struct {
	Port    int       `json:"port"`
	Name    string    `json:"name"`
	Updated time.Time `json:"updated"`
}

// lockFile serializes changes to shared state such as the tunnel ports.
func lockFile(path string) (func(), error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, err
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, err
	}
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX); err != nil {
		f.Close()
		return nil, err
	}
	return func() { syscall.Flock(int(f.Fd()), syscall.LOCK_UN); f.Close() }, nil
}

func readTunnels() (map[string]*tunnelEntry, error) {
	t := map[string]*tunnelEntry{}
	b, err := os.ReadFile(tunnelsFile)
	if errors.Is(err, os.ErrNotExist) {
		return t, nil
	}
	if err != nil {
		return nil, err
	}
	return t, json.Unmarshal(b, &t)
}

func checkMac(id string) error {
	if !macIDRe.MatchString(id) {
		return proto.Errf(proto.CodeUsage, "", "invalid Mac id %q", id)
	}
	return nil
}

// cmdTunnel returns the port a Mac's tunnel binds to, allocating one on first
// use. A dropped connection can leave the old sshd session holding the port
// until TCP gives up on it, so with --reconnect that session is closed before
// the Mac connects again.
func cmdTunnel(args []string) (any, error) {
	fs := newFlags("tunnel")
	mac := fs.String("mac", "", "")
	macName := fs.String("mac-name", "", "")
	reconnect := fs.Bool("reconnect", false, "")
	if err := parse(fs, args); err != nil {
		return nil, err
	}
	if err := checkMac(*mac); err != nil {
		return nil, err
	}
	cfg, err := loadServer("")
	if err != nil {
		return nil, err
	}
	dev, err := devDomain(cfg)
	if err != nil {
		return nil, err
	}
	unlock, err := lockFile(tunnelsFile + ".lock")
	if err != nil {
		return nil, err
	}
	defer unlock()
	tunnels, err := readTunnels()
	if err != nil {
		return nil, err
	}
	t := tunnels[*mac]
	if t == nil {
		used := map[int]bool{}
		for _, o := range tunnels {
			used[o.Port] = true
		}
		for p := tunnelPortStart; p <= tunnelPortEnd && t == nil; p++ {
			if !used[p] && portFree(p) {
				t = &tunnelEntry{Port: p}
			}
		}
		if t == nil {
			return nil, proto.Errf(proto.CodeInternal, "", "no free tunnel ports in %d-%d", tunnelPortStart, tunnelPortEnd)
		}
		tunnels[*mac] = t
	}
	t.Name, t.Updated = cleanName(*macName), time.Now().UTC()
	if err := writeJSON(tunnelsFile, tunnels); err != nil {
		return nil, err
	}
	if *reconnect {
		closeStaleTunnel(t.Port)
	}
	return &proto.Tunnel{Port: t.Port, DevDomain: dev}, nil
}

// closeStaleTunnel ends the sshd session that still listens on port, if any.
func closeStaleTunnel(port int) {
	out, _ := output("ss", "-ltnpH", "sport = :"+strconv.Itoa(port))
	// the session process is "sshd-session" since OpenSSH 9.8
	if m := ssUserRe.FindStringSubmatch(out); m == nil || !strings.HasPrefix(m[1], "sshd") {
		return
	}
	if m := ssPIDRe.FindStringSubmatch(out); m != nil {
		if pid, _ := strconv.Atoi(m[1]); pid > 1 {
			progress("Closing the previous tunnel connection")
			syscall.Kill(pid, syscall.SIGTERM)
			for i := 0; i < 20 && !portFree(port); i++ {
				time.Sleep(100 * time.Millisecond)
			}
		}
	}
}

func cleanName(s string) string {
	s = strings.TrimSpace(unsafeRe.ReplaceAllString(s, ""))
	if r := []rune(s); len(r) > 60 {
		s = string(r[:60])
	}
	return s
}

func shareFile(name string) string { return filepath.Join(sharesDir, name+".json") }

// shareKey names the share's proxy config. App names cannot start with "_",
// so it never collides with an app.
func shareKey(name string) string { return "_dev-" + name }

func loadShare(name string) (*proto.Share, error) {
	b, err := os.ReadFile(shareFile(name))
	if err != nil {
		return nil, err
	}
	var s proto.Share
	return &s, json.Unmarshal(b, &s)
}

func listShares() ([]*proto.Share, error) {
	entries, err := os.ReadDir(sharesDir)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var out []*proto.Share
	for _, e := range entries {
		if name, ok := strings.CutSuffix(e.Name(), ".json"); ok {
			if s, err := loadShare(name); err == nil {
				out = append(out, s)
			}
		}
	}
	return out, nil
}

// share: shipd share add|rm|list ...
func cmdShare(args []string) (any, error) {
	if len(args) == 0 {
		return nil, proto.Errf(proto.CodeUsage, "", "usage: share add|rm|list ...")
	}
	op := args[0]
	fs := newFlags("share " + op)
	name := fs.String("name", "", "")
	mac := fs.String("mac", "", "")
	macName := fs.String("mac-name", "", "")
	force := fs.Bool("force", false, "remove even if another Mac shares it")
	if err := parse(fs, args[1:]); err != nil {
		return nil, err
	}
	if op == "list" {
		shares, err := listShares()
		if shares == nil {
			shares = []*proto.Share{}
		}
		return map[string]any{"shares": shares}, err
	}
	if err := checkAppName(*name); err != nil {
		return nil, err
	}
	if err := checkMac(*mac); err != nil {
		return nil, err
	}
	cfg, err := loadServer("")
	if err != nil {
		return nil, err
	}
	unlock, err := lockFile(filepath.Join(sharesDir, ".lock"))
	if err != nil {
		return nil, err
	}
	defer unlock()
	switch op {
	case "add":
		return shareAdd(cfg, *name, *mac, cleanName(*macName))
	case "rm":
		return shareRemove(cfg, *name, *mac, *force)
	}
	return nil, proto.Errf(proto.CodeUsage, "", "unknown share operation %q", op)
}

func shareAdd(cfg *serverConfig, name, mac, macName string) (*proto.Share, error) {
	dev, err := devDomain(cfg)
	if err != nil {
		return nil, err
	}
	tunnels, err := readTunnels()
	if err != nil {
		return nil, err
	}
	t := tunnels[mac]
	if t == nil {
		return nil, proto.Errf(proto.CodeConfig, "", "this Mac has no tunnel to the server yet")
	}
	host := name + "." + dev
	old, err := loadShare(name)
	if err == nil && old.Mac != mac {
		return nil, proto.Errf(proto.CodeShareTaken, "unshare it on "+old.MacName+" first, or rename this project (name in homebase.toml)",
			"%s is already shared from %s", host, old.MacName)
	}
	apps, _ := listApps()
	for _, a := range apps {
		if a.Domain == host {
			return nil, proto.Errf(proto.CodeShareTaken, "rename this project (name in homebase.toml)", "%s is the domain of the deployed app %q", host, a.Name)
		}
	}
	sh := &proto.Share{Name: name, Host: host, Mac: mac, MacName: macName, CreatedAt: time.Now().UTC()}
	if old != nil && old.Host == host {
		sh.CreatedAt, sh.CertByShip = old.CreatedAt, old.CertByShip
	}
	if old != nil && old.Host != host {
		unrouteShare(cfg, old) // the dev domain changed
	}
	if sh.Warnings, err = routeShare(cfg, sh, t.Port); err != nil {
		return nil, err
	}
	if err := os.MkdirAll(sharesDir, 0o700); err != nil {
		return nil, err
	}
	return sh, writeJSON(shareFile(name), sh)
}

func shareRemove(cfg *serverConfig, name, mac string, force bool) (any, error) {
	sh, err := loadShare(name)
	if errors.Is(err, os.ErrNotExist) {
		return map[string]any{"name": name, "removed": false}, nil
	}
	if err != nil {
		return nil, err
	}
	if sh.Mac != mac && !force {
		return nil, proto.Errf(proto.CodeShareTaken, "", "%s is shared from %s, not from this Mac", sh.Host, sh.MacName)
	}
	if err := unrouteShare(cfg, sh); err != nil {
		return nil, err
	}
	if err := os.Remove(shareFile(name)); err != nil {
		return nil, err
	}
	return map[string]any{"name": name, "host": sh.Host, "removed": true}, nil
}

func routeShare(cfg *serverConfig, sh *proto.Share, port int) ([]string, error) {
	if cfg.proxy() != proxyCaddy {
		a := &proto.App{Name: shareKey(sh.Name), Domain: sh.Host, CertByShip: sh.CertByShip}
		warnings, err := nginxRoute(cfg, a, port)
		sh.URL, sh.TLS, sh.CertByShip = a.URL, a.TLS, a.CertByShip
		return warnings, err
	}
	if err := ensureCaddyfile(); err != nil {
		return nil, err
	}
	progress("Routing https://%s → this Mac", sh.Host)
	site := fmt.Sprintf(`# managed by homebase: %s, shared from %s (do not edit)
%s {
	reverse_proxy 127.0.0.1:%d
	handle_errors {
		respond "%s is offline: the Mac that shares it (%s) is asleep or not connected." 502
	}
}
`, sh.Name, sh.MacName, sh.Host, port, sh.Host, sh.MacName)
	if err := applyCaddy(caddyConfPath(shareKey(sh.Name)), []byte(site)); err != nil {
		return nil, err
	}
	sh.URL = "https://" + sh.Host
	if sh.TLS = haveValidTLS(sh.Host, 2*time.Second); !sh.TLS {
		progress("Waiting for the TLS certificate")
		sh.TLS = waitTLS(sh.Host, 60*time.Second)
	}
	if sh.TLS {
		return nil, nil
	}
	w := "TLS certificate is not ready yet; Caddy keeps retrying in the background"
	if ok, why := dnsPointsHere(sh.Host, cfg.PublicIP); !ok {
		w += fmt.Sprintf(" (%s; add a DNS record *.%s → %s)", why, strings.TrimPrefix(sh.Host, sh.Name+"."), cfg.PublicIP)
	}
	return []string{w}, nil
}

// rehostShares moves the shares to the current dev domain.
func rehostShares(cfg *serverConfig) {
	dev := devDomainOrEmpty(cfg)
	shares, _ := listShares()
	tunnels, _ := readTunnels()
	for _, sh := range shares {
		t := tunnels[sh.Mac]
		if dev == "" || t == nil || sh.Host == sh.Name+"."+dev {
			continue
		}
		progress("Moving the share %s to %s.%s", sh.Host, sh.Name, dev)
		unrouteShare(cfg, sh)
		sh.Host, sh.CertByShip = sh.Name+"."+dev, false
		if _, err := routeShare(cfg, sh, t.Port); err != nil {
			progress("  failed: %v", err)
			continue
		}
		writeJSON(shareFile(sh.Name), sh)
	}
}

func unrouteShare(cfg *serverConfig, sh *proto.Share) error {
	a := &proto.App{Name: shareKey(sh.Name), Domain: sh.Host, CertByShip: sh.CertByShip}
	return unroute(cfg, a)
}
