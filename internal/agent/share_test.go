package agent

import "testing"

func TestDevDomain(t *testing.T) {
	for _, c := range []struct {
		cfg  serverConfig
		want string
	}{
		{serverConfig{BaseDomain: "example.com", PublicIP: "1.2.3.4"}, "dev.example.com"},
		{serverConfig{BaseDomain: "example.com", DevDomain: "preview.example.org"}, "preview.example.org"},
		{serverConfig{PublicIP: "1.2.3.4"}, "dev.1-2-3-4.sslip.io"},
		{serverConfig{}, ""},
	} {
		if got := devDomainOrEmpty(&c.cfg); got != c.want {
			t.Errorf("%+v: %q, want %q", c.cfg, got, c.want)
		}
	}
}

func TestCleanName(t *testing.T) {
	if got := cleanName("Vlad’s \"Mac\" {x}\n"); got != "Vlad’s Mac x" {
		t.Errorf("got %q", got)
	}
}
