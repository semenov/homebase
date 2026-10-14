package agent

import (
	"strings"
	"testing"
)

func TestReverseProxyKeepsStreams(t *testing.T) {
	caddyVersionOnce.Do(func() {})
	defer func() { caddyMajor, caddyMinor = 0, 0 }()

	caddyMajor, caddyMinor = 2, 11
	site := string(renderCaddySite("hangar", "hangar.example.com", 20003))
	if !strings.Contains(site, "reverse_proxy 127.0.0.1:20003 {\n\t\tstream_close_delay 24h\n\t}") {
		t.Errorf("Caddy 2.11:\n%s", site)
	}
	caddyMajor, caddyMinor = 2, 6 // no stream_close_delay before 2.7
	if site := string(renderCaddySite("x", "x.example.com", 1)); strings.Contains(site, "stream_close_delay") || !strings.Contains(site, "reverse_proxy 127.0.0.1:1\n") {
		t.Errorf("Caddy 2.6:\n%s", site)
	}
}
