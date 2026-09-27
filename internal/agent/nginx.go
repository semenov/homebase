package agent

import (
	"bytes"
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"text/template"

	"github.com/vsemenov/ship/internal/proto"
)

func siteConfPath(cfg *serverConfig, app string) string {
	return filepath.Join(cfg.NginxDir, "ship-"+app+".conf")
}

func certPaths(domain string) (fullchain, key string) {
	dir := filepath.Join("/etc/letsencrypt/live", domain)
	return filepath.Join(dir, "fullchain.pem"), filepath.Join(dir, "privkey.pem")
}

func haveCert(domain string) bool {
	full, key := certPaths(domain)
	_, err1 := os.Stat(full)
	_, err2 := os.Stat(key)
	return err1 == nil && err2 == nil
}

var siteTmpl = template.Must(template.New("site").Parse(`# managed by ship: app {{.App}} (do not edit, rewritten on every deploy)
{{define "proxy"}}
    client_max_body_size 100m;

    location / {
        proxy_pass http://127.0.0.1:{{.Port}};
        proxy_http_version 1.1;
        proxy_set_header Host $host;
        proxy_set_header X-Real-IP $remote_addr;
        proxy_set_header X-Forwarded-For $proxy_add_x_forwarded_for;
        proxy_set_header X-Forwarded-Proto $scheme;
        proxy_set_header Upgrade $http_upgrade;
        proxy_set_header Connection $ship_connection_upgrade;
        proxy_read_timeout 300s;
        proxy_send_timeout 300s;
    }
{{- end}}
server {
{{- range .HTTPListen}}
    listen {{.}};
{{- end}}
    server_name {{.Domain}};

    location /.well-known/acme-challenge/ {
        root {{.Webroot}};
    }
{{- if .TLS}}

    location / {
        return 301 https://$host$request_uri;
    }
{{- else}}
{{template "proxy" .}}
{{- end}}
}
{{- if .TLS}}

server {
{{- range .SSLListen}}
    listen {{.}};
{{- end}}
    server_name {{.Domain}};

    ssl_certificate {{.Fullchain}};
    ssl_certificate_key {{.Key}};
{{- range .SSLOptions}}
    {{.}}
{{- end}}
{{template "proxy" .}}
}
{{- end}}
`))

func renderSite(cfg *serverConfig, app, domain string, port int, tls bool) []byte {
	full, key := certPaths(domain)
	var b bytes.Buffer
	siteTmpl.Execute(&b, map[string]any{
		"App": app, "Domain": domain, "Port": port, "TLS": tls,
		"HTTPListen": cfg.HTTPListen, "SSLListen": cfg.SSLListen, "SSLOptions": cfg.SSLOptions,
		"Webroot": acmeWebroot, "Fullchain": full, "Key": key,
	})
	return b.Bytes()
}

// applyNginx writes a config file, validates the whole nginx config and reloads.
// If validation fails the previous file (or its absence) is restored, so a bad
// ship config can never take down the other sites on the server.
func applyNginx(path string, content []byte) error {
	prev, prevErr := os.ReadFile(path)
	if prevErr == nil && bytes.Equal(prev, content) {
		return nil
	}
	if err := writeFileAtomic(path, content, 0o644); err != nil {
		return err
	}
	if out, err := combined("nginx", "-t"); err != nil {
		if prevErr == nil {
			writeFileAtomic(path, prev, 0o644)
		} else {
			os.Remove(path)
		}
		return &proto.Error{Code: proto.CodeProxy, Message: "nginx rejected the generated config; previous config restored", Logs: tail(out, 20)}
	}
	return reloadNginx()
}

func removeNginx(path string) error {
	if err := os.Remove(path); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		return err
	}
	return reloadNginx()
}

func reloadNginx() error {
	out, err := combined("systemctl", "reload", "nginx")
	if err != nil {
		if out2, err2 := combined("nginx", "-s", "reload"); err2 != nil {
			return &proto.Error{Code: proto.CodeProxy, Message: fmt.Sprintf("nginx reload failed: %v", err2), Logs: tail(out+out2, 20)}
		}
	}
	return nil
}

// nginxRoute points the app's domain at hostPort, obtaining a certificate with
// certbot if needed. TLS problems are warnings, not failures: the app stays
// reachable over http.
func nginxRoute(cfg *serverConfig, a *proto.App, hostPort int) ([]string, error) {
	path := siteConfPath(cfg, a.Name)
	var warnings []string
	a.TLS = haveCert(a.Domain)
	if !a.TLS {
		progress("Configuring nginx for http://%s", a.Domain)
		if err := applyNginx(path, renderSite(cfg, a.Name, a.Domain, hostPort, false)); err != nil {
			return nil, err
		}
		if ok, why := dnsPointsHere(a.Domain, cfg.PublicIP); !ok {
			warnings = append(warnings, fmt.Sprintf("no TLS: %s; add an A record %s → %s and redeploy", why, a.Domain, cfg.PublicIP))
		} else {
			progress("Requesting TLS certificate for %s", a.Domain)
			if err := obtainCert(a.Domain); err != nil {
				warnings = append(warnings, "no TLS: certbot failed: "+err.Error())
			} else {
				a.TLS, a.CertByShip = true, true
			}
		}
	}
	if a.TLS {
		progress("Configuring nginx for https://%s", a.Domain)
		if err := applyNginx(path, renderSite(cfg, a.Name, a.Domain, hostPort, true)); err != nil {
			return nil, err
		}
	}
	a.URL = "http://" + a.Domain
	if a.TLS {
		a.URL = "https://" + a.Domain
	}
	return warnings, nil
}

func dnsPointsHere(domain, ip string) (bool, string) {
	if ip == "" {
		return true, ""
	}
	addrs, err := net.LookupHost(domain)
	if err != nil || len(addrs) == 0 {
		return false, domain + " does not resolve"
	}
	if !slices.Contains(addrs, ip) {
		return false, fmt.Sprintf("%s resolves to %s, not to this server", domain, strings.Join(addrs, ", "))
	}
	return true, ""
}

func obtainCert(domain string) error {
	if err := os.MkdirAll(acmeWebroot, 0o755); err != nil {
		return err
	}
	args := []string{"certonly", "--webroot", "-w", acmeWebroot, "-d", domain,
		"--non-interactive", "--agree-tos", "--keep-until-expiring",
		"--deploy-hook", "systemctl reload nginx"}
	if entries, _ := os.ReadDir("/etc/letsencrypt/accounts"); len(entries) == 0 {
		args = append(args, "--register-unsafely-without-email")
	}
	out, err := combined("certbot", args...)
	if err != nil {
		return errors.New(tail(out, 5))
	}
	return nil
}

func nginxUnroute(cfg *serverConfig, a *proto.App) error {
	if err := removeNginx(siteConfPath(cfg, a.Name)); err != nil {
		return err
	}
	if a.CertByShip {
		progress("Deleting certificate for %s", a.Domain)
		combined("certbot", "delete", "--cert-name", a.Domain, "--non-interactive")
	}
	return nil
}
