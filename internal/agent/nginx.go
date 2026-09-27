package agent

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"path/filepath"
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
