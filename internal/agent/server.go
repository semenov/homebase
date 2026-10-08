package agent

import (
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"

	"github.com/semenov/homebase/internal/proto"
)

type serverConfig struct {
	Proxy      string `json:"proxy"` // caddy | nginx ("" in old configs means nginx)
	PublicIP   string `json:"public_ip"`
	BaseDomain string `json:"base_domain,omitempty"`
	// DevDomain overrides where shares go (default: dev.<BaseDomain>)
	DevDomain string `json:"dev_domain,omitempty"`
	// WildcardDNS is the DNS provider used for the *.BaseDomain certificate ("" = per-app certificates)
	WildcardDNS string `json:"wildcard_dns,omitempty"`
	// nginx only
	NginxDir   string   `json:"nginx_dir,omitempty"`
	SSLListen  []string `json:"ssl_listen,omitempty"`
	HTTPListen []string `json:"http_listen,omitempty"`
	SSLOptions []string `json:"ssl_options,omitempty"`
}

func (c *serverConfig) proxy() string {
	if c.Proxy == "" {
		return proxyNginx
	}
	return c.Proxy
}

const commonConfName = "ship-000-common.conf"

var (
	sniDefaultRe = regexp.MustCompile(`default\s+(127\.0\.0\.1:\d+)\s*;`)
	routeSrcRe   = regexp.MustCompile(`\bsrc\s+(\S+)`)
	ssUserRe     = regexp.MustCompile(`users:\(\("([^"]+)"`)
)

func cmdSetup(args []string) (any, error) {
	fs := newFlags("setup")
	ip := fs.String("ip", "", "public IP of the server")
	base := fs.String("base-domain", "", "wildcard domain for apps, e.g. apps.example.com")
	install := fs.Bool("install", false, "install missing docker and caddy")
	wildcard := fs.String("wildcard", "", "DNS provider for a *.base-domain certificate (token on stdin)")
	dev := fs.String("dev-domain", "", "domain for shares from Macs, e.g. dev.example.com")
	if err := parse(fs, args); err != nil {
		return nil, err
	}
	if *install {
		if err := installDeps(); err != nil {
			return nil, err
		}
	}
	old, _ := readServerConfig()
	cfg, err := detectServer(*ip)
	if err != nil {
		return nil, err
	}
	cfg.BaseDomain = strings.TrimPrefix(strings.ToLower(*base), "*.")
	if cfg.BaseDomain == "" && old != nil {
		cfg.BaseDomain = old.BaseDomain
	}
	if old != nil && old.BaseDomain == cfg.BaseDomain {
		cfg.WildcardDNS = old.WildcardDNS
	}
	cfg.DevDomain = strings.TrimPrefix(strings.ToLower(*dev), "*.")
	if cfg.DevDomain == "" && old != nil {
		cfg.DevDomain = old.DevDomain
	}
	if err := prepareProxy(cfg); err != nil {
		return nil, err
	}
	if err := os.MkdirAll(stateDir, 0o700); err != nil {
		return nil, err
	}
	if *wildcard != "" {
		if err := setupWildcard(cfg, *wildcard, os.Stdin); err != nil {
			return nil, err
		}
	}
	if err := writeJSON(serverFile, cfg); err != nil {
		return nil, err
	}
	if old != nil && old.proxy() != cfg.proxy() {
		progress("Proxy changed from %s to %s", old.proxy(), cfg.proxy())
		if old.proxy() == proxyNginx {
			removeNginxShipConfs(old.NginxDir)
		}
		reroute(cfg)
	}
	return serverInfo(cfg), nil
}

func serverInfo(cfg *serverConfig) *proto.ServerInfo {
	apps, _ := listApps()
	docker, _ := output("docker", "version", "--format", "{{.Server.Version}}")
	var version []byte
	if cfg.proxy() == proxyCaddy {
		version, _ = exec.Command("caddy", "version").Output()
		version, _, _ = bytes.Cut(version, []byte(" "))
	} else {
		version, _ = exec.Command("nginx", "-v").CombinedOutput()
		version = bytes.TrimPrefix(version, []byte("nginx version: "))
	}
	return &proto.ServerInfo{
		Arch:         runtime.GOARCH,
		PublicIP:     cfg.PublicIP,
		BaseDomain:   cfg.BaseDomain,
		DevDomain:    devDomainOrEmpty(cfg),
		WildcardDNS:  cfg.WildcardDNS,
		Proxy:        cfg.proxy(),
		ProxyVersion: strings.TrimSpace(string(version)),
		NginxDir:     cfg.NginxDir,
		SSLListen:    cfg.SSLListen,
		Docker:       strings.TrimSpace(docker),
		Apps:         len(apps),
	}
}

func readServerConfig() (*serverConfig, error) {
	b, err := os.ReadFile(serverFile)
	if err != nil {
		return nil, err
	}
	var cfg serverConfig
	return &cfg, json.Unmarshal(b, &cfg)
}

// loadServer returns the saved server config, detecting it on first use so that
// `homebase deploy` works on a server that already has docker and a proxy without `homebase server add`.
func loadServer(ip string) (*serverConfig, error) {
	cfg, err := readServerConfig()
	if err == nil {
		if ip != "" && cfg.PublicIP != ip {
			cfg.PublicIP = ip
			writeJSON(serverFile, cfg)
		}
		return cfg, nil
	}
	if !errors.Is(err, os.ErrNotExist) {
		return nil, err
	}
	progress("First deploy to this server: detecting setup")
	if cfg, err = detectServer(ip); err != nil {
		return nil, err
	}
	if err := prepareProxy(cfg); err != nil {
		return nil, err
	}
	if err := os.MkdirAll(stateDir, 0o700); err != nil {
		return nil, err
	}
	return cfg, writeJSON(serverFile, cfg)
}

const installHint = "run `homebase server add <host> --install` to install docker and caddy"

// port443Owner returns the name of the process listening on :443, or "".
func port443Owner() string {
	out, _ := output("ss", "-ltnpH", "sport = :443")
	if m := ssUserRe.FindStringSubmatch(out); m != nil {
		return m[1]
	}
	return ""
}

// detectProxy picks the reverse proxy: whoever serves :443 now, else an
// installed Caddy (preferred) or nginx.
func detectProxy() (string, error) {
	owner := port443Owner()
	switch {
	case owner == "caddy":
		return proxyCaddy, nil
	case owner == "nginx":
		return proxyNginx, nil
	case owner != "":
		return "", proto.Errf(proto.CodeServerNotReady, "homebase works with caddy or nginx on :443", "port 443 is used by %q", owner)
	}
	if _, err := exec.LookPath("caddy"); err == nil {
		progress("Starting caddy")
		if out, err := combined("systemctl", "enable", "--now", "caddy"); err != nil {
			return "", &proto.Error{Code: proto.CodeServerNotReady, Message: "caddy is installed but does not start", Logs: tail(out, 20)}
		}
		return proxyCaddy, nil
	}
	if _, err := exec.LookPath("nginx"); err == nil {
		return proxyNginx, nil
	}
	return "", proto.Errf(proto.CodeServerNotReady, installHint, "no reverse proxy (caddy or nginx) on the server")
}

func prepareProxy(cfg *serverConfig) error {
	if cfg.proxy() == proxyCaddy {
		return ensureCaddyfile()
	}
	return writeCommonConf(cfg)
}

func installDeps() error {
	if _, err := exec.LookPath("apt-get"); err != nil {
		return proto.Errf(proto.CodeServerNotReady, "install docker and caddy manually", "automatic install supports only Debian/Ubuntu")
	}
	if _, err := exec.LookPath("docker"); err != nil {
		progress("Installing docker")
		if out, err := combined("sh", "-c", "curl -fsSL https://get.docker.com | sh"); err != nil {
			return &proto.Error{Code: proto.CodeServerNotReady, Message: "docker install failed", Logs: tail(out, 30)}
		}
	}
	if _, err := exec.LookPath("caddy"); err != nil && port443Owner() == "" {
		progress("Installing caddy")
		script := `set -e
export DEBIAN_FRONTEND=noninteractive
apt-get update -qq
apt-get install -y -qq debian-keyring debian-archive-keyring apt-transport-https curl gpg
curl -1sLf https://dl.cloudsmith.io/public/caddy/stable/gpg.key | gpg --batch --yes --dearmor -o /usr/share/keyrings/caddy-stable-archive-keyring.gpg
curl -1sLf https://dl.cloudsmith.io/public/caddy/stable/debian.deb.txt > /etc/apt/sources.list.d/caddy-stable.list
apt-get update -qq
apt-get install -y -qq caddy`
		if out, err := combined("sh", "-c", script); err != nil {
			return &proto.Error{Code: proto.CodeServerNotReady, Message: "caddy install failed", Logs: tail(out, 30)}
		}
	}
	return nil
}

func detectServer(ip string) (*serverConfig, error) {
	if _, err := output("docker", "version", "--format", "{{.Server.Version}}"); err != nil {
		return nil, proto.Errf(proto.CodeServerNotReady, installHint, "docker is not available on the server")
	}
	proxy, err := detectProxy()
	if err != nil {
		return nil, err
	}
	cfg := &serverConfig{Proxy: proxy, PublicIP: ip}
	if cfg.PublicIP == "" {
		if out, err := output("ip", "-4", "route", "get", "1.1.1.1"); err == nil {
			if m := routeSrcRe.FindStringSubmatch(out); m != nil {
				cfg.PublicIP = m[1]
			}
		}
	}
	if proxy == proxyNginx {
		return cfg, detectNginx(cfg)
	}
	return cfg, nil
}

func detectNginx(cfg *serverConfig) error {
	if _, err := exec.LookPath("certbot"); err != nil {
		return proto.Errf(proto.CodeServerNotReady, "install certbot (apt-get install certbot)", "nginx mode needs certbot, which is not installed")
	}
	dump, err := combined("nginx", "-T")
	if err != nil {
		return &proto.Error{Code: proto.CodeServerNotReady, Message: "current nginx config is invalid (nginx -T failed); fix it before deploying", Logs: tail(dump, 20)}
	}

	// An SNI router (stream + ssl_preread) in front of the http vhosts means ssl
	// vhosts must listen on its default backend instead of :443.
	if backend := sniDefaultBackend(dump); backend != "" {
		cfg.SSLListen = []string{backend + " ssl"}
	} else {
		cfg.SSLListen = []string{"443 ssl"}
		if strings.Contains(dump, "[::]:443") {
			cfg.SSLListen = append(cfg.SSLListen, "[::]:443 ssl")
		}
	}
	cfg.HTTPListen = []string{"80"}
	if strings.Contains(dump, "[::]:80") {
		cfg.HTTPListen = append(cfg.HTTPListen, "[::]:80")
	}

	cfg.NginxDir = "/etc/nginx/conf.d"
	if fi, err := os.Stat("/etc/nginx/sites-enabled"); err == nil && fi.IsDir() && strings.Contains(dump, "sites-enabled") {
		cfg.NginxDir = "/etc/nginx/sites-enabled"
	}
	if _, err := os.Stat("/etc/letsencrypt/options-ssl-nginx.conf"); err == nil {
		cfg.SSLOptions = append(cfg.SSLOptions, "include /etc/letsencrypt/options-ssl-nginx.conf;")
	}
	if _, err := os.Stat("/etc/letsencrypt/ssl-dhparams.pem"); err == nil {
		cfg.SSLOptions = append(cfg.SSLOptions, "ssl_dhparam /etc/letsencrypt/ssl-dhparams.pem;")
	}
	return nil
}

func sniDefaultBackend(dump string) string {
	i := strings.Index(dump, "ssl_preread")
	if i < 0 {
		return ""
	}
	// look for the `default` entry of the map inside the stream block
	start := strings.LastIndex(dump[:i], "stream")
	if start < 0 {
		start = 0
	}
	if m := sniDefaultRe.FindStringSubmatch(dump[start:]); m != nil {
		return m[1]
	}
	return ""
}

func writeCommonConf(cfg *serverConfig) error {
	path := filepath.Join(cfg.NginxDir, commonConfName)
	content := "# managed by ship\nmap $http_upgrade $ship_connection_upgrade {\n    default upgrade;\n    ''      close;\n}\n"
	if b, err := os.ReadFile(path); err == nil && string(b) == content {
		return nil
	}
	return applyNginx(path, []byte(content))
}
