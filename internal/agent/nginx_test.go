package agent

import (
	"strings"
	"testing"
)

const sniDump = `
http { include /etc/nginx/sites-enabled/*; }
stream {
    map $ssl_preread_server_name $backend {
        hello.example.com  127.0.0.1:8443;
        default           127.0.0.1:8444;
    }
    server { listen 443; proxy_pass $backend; ssl_preread on; }
}`

func TestSNIDefaultBackend(t *testing.T) {
	if got := sniDefaultBackend(sniDump); got != "127.0.0.1:8444" {
		t.Fatalf("got %q", got)
	}
	if got := sniDefaultBackend("server { listen 443 ssl; }"); got != "" {
		t.Fatalf("got %q", got)
	}
}

func TestRenderSite(t *testing.T) {
	cfg := &serverConfig{HTTPListen: []string{"80", "[::]:80"}, SSLListen: []string{"127.0.0.1:8444 ssl"},
		SSLOptions: []string{"include /etc/letsencrypt/options-ssl-nginx.conf;"}}

	plain := string(renderSite(cfg, "app", "app.example.com", 20001, false))
	for _, want := range []string{"listen 80;", "listen [::]:80;", "proxy_pass http://127.0.0.1:20001;", "acme-challenge"} {
		if !strings.Contains(plain, want) {
			t.Errorf("http config lacks %q:\n%s", want, plain)
		}
	}
	if strings.Contains(plain, "ssl_certificate") {
		t.Error("http config must not reference certificates")
	}

	tls := string(renderSite(cfg, "app", "app.example.com", 20001, true))
	for _, want := range []string{"listen 127.0.0.1:8444 ssl;", "return 301 https://", "/etc/letsencrypt/live/app.example.com/fullchain.pem",
		"options-ssl-nginx.conf", "proxy_pass http://127.0.0.1:20001;", "$ship_connection_upgrade"} {
		if !strings.Contains(tls, want) {
			t.Errorf("tls config lacks %q:\n%s", want, tls)
		}
	}
	if strings.Count(tls, "proxy_pass") != 1 {
		t.Error("tls config should proxy only from the ssl server")
	}
}
