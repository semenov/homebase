package agent

import (
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"

	"github.com/vsemenov/ship/internal/proto"
)

type serverConfig struct {
	PublicIP   string   `json:"public_ip"`
	BaseDomain string   `json:"base_domain,omitempty"`
	NginxDir   string   `json:"nginx_dir"`
	SSLListen  []string `json:"ssl_listen"`
	HTTPListen []string `json:"http_listen"`
	SSLOptions []string `json:"ssl_options,omitempty"`
}

const commonConfName = "ship-000-common.conf"

var (
	sniDefaultRe = regexp.MustCompile(`default\s+(127\.0\.0\.1:\d+)\s*;`)
	routeSrcRe   = regexp.MustCompile(`\bsrc\s+(\S+)`)
)

func cmdSetup(args []string) (any, error) {
	fs := newFlags("setup")
	ip := fs.String("ip", "", "public IP of the server")
	base := fs.String("base-domain", "", "wildcard domain for apps, e.g. apps.example.com")
	install := fs.Bool("install", false, "install missing docker/nginx/certbot")
	if err := parse(fs, args); err != nil {
		return nil, err
	}
	if *install {
		if err := installDeps(); err != nil {
			return nil, err
		}
	}
	if err := checkDeps(); err != nil {
		return nil, err
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
	if err := writeCommonConf(cfg); err != nil {
		return nil, err
	}
	if err := os.MkdirAll(stateDir, 0o700); err != nil {
		return nil, err
	}
	if err := writeJSON(serverFile, cfg); err != nil {
		return nil, err
	}
	return serverInfo(cfg), nil
}

func serverInfo(cfg *serverConfig) *proto.ServerInfo {
	apps, _ := listApps()
	docker, _ := output("docker", "version", "--format", "{{.Server.Version}}")
	nginx, _ := exec.Command("nginx", "-v").CombinedOutput()
	_, certErr := exec.LookPath("certbot")
	return &proto.ServerInfo{
		Arch:       runtime.GOARCH,
		PublicIP:   cfg.PublicIP,
		BaseDomain: cfg.BaseDomain,
		NginxDir:   cfg.NginxDir,
		SSLListen:  cfg.SSLListen,
		HTTPListen: cfg.HTTPListen,
		Docker:     strings.TrimSpace(docker),
		Nginx:      strings.TrimPrefix(strings.TrimSpace(string(nginx)), "nginx version: "),
		Certbot:    certErr == nil,
		Apps:       len(apps),
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
// `ship deploy` works on a server that already has docker and nginx without `ship init`.
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
	if err := checkDeps(); err != nil {
		return nil, err
	}
	progress("First deploy to this server: detecting nginx setup")
	if cfg, err = detectServer(ip); err != nil {
		return nil, err
	}
	if err := writeCommonConf(cfg); err != nil {
		return nil, err
	}
	if err := os.MkdirAll(stateDir, 0o700); err != nil {
		return nil, err
	}
	return cfg, writeJSON(serverFile, cfg)
}

func checkDeps() error {
	hint := "run `ship init <host> --install` to install docker, nginx and certbot"
	if _, err := output("docker", "version", "--format", "{{.Server.Version}}"); err != nil {
		return proto.Errf(proto.CodeServerNotReady, hint, "docker is not available on the server")
	}
	if _, err := exec.LookPath("nginx"); err != nil {
		return proto.Errf(proto.CodeServerNotReady, hint, "nginx is not installed on the server")
	}
	if _, err := exec.LookPath("certbot"); err != nil {
		return proto.Errf(proto.CodeServerNotReady, hint, "certbot is not installed on the server")
	}
	return nil
}

func installDeps() error {
	if _, err := exec.LookPath("apt-get"); err != nil {
		return proto.Errf(proto.CodeServerNotReady, "install docker, nginx and certbot manually", "automatic install supports only Debian/Ubuntu")
	}
	var pkgs []string
	if _, err := exec.LookPath("nginx"); err != nil {
		pkgs = append(pkgs, "nginx")
	}
	if _, err := exec.LookPath("certbot"); err != nil {
		pkgs = append(pkgs, "certbot")
	}
	if len(pkgs) > 0 {
		progress("Installing %s", strings.Join(pkgs, ", "))
		if out, err := combined("sh", "-c", "DEBIAN_FRONTEND=noninteractive apt-get update -qq && DEBIAN_FRONTEND=noninteractive apt-get install -y -qq "+strings.Join(pkgs, " ")); err != nil {
			return &proto.Error{Code: proto.CodeServerNotReady, Message: "apt-get install failed", Logs: tail(out, 30)}
		}
		combined("systemctl", "enable", "--now", "nginx")
	}
	if _, err := exec.LookPath("docker"); err != nil {
		progress("Installing docker")
		if out, err := combined("sh", "-c", "curl -fsSL https://get.docker.com | sh"); err != nil {
			return &proto.Error{Code: proto.CodeServerNotReady, Message: "docker install failed", Logs: tail(out, 30)}
		}
	}
	return nil
}

func detectServer(ip string) (*serverConfig, error) {
	dump, err := combined("nginx", "-T")
	if err != nil {
		return nil, &proto.Error{Code: proto.CodeServerNotReady, Message: "current nginx config is invalid (nginx -T failed); fix it before deploying", Logs: tail(dump, 20)}
	}
	cfg := &serverConfig{PublicIP: ip}
	if cfg.PublicIP == "" {
		if out, err := output("ip", "-4", "route", "get", "1.1.1.1"); err == nil {
			if m := routeSrcRe.FindStringSubmatch(out); m != nil {
				cfg.PublicIP = m[1]
			}
		}
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
	return cfg, nil
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
