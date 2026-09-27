package cli

import "testing"

func TestShellQuote(t *testing.T) {
	for in, want := range map[string]string{
		"deploy":          "deploy",
		"ship/app:1":      "ship/app:1",
		"":                "''",
		"K=a b":           "'K=a b'",
		"it's":            `'it'"'"'s'`,
		"$(rm -rf /)":     "'$(rm -rf /)'",
		"GREETING=привет": "'GREETING=привет'",
	} {
		if got := shellQuote(in); got != want {
			t.Errorf("shellQuote(%q) = %s, want %s", in, got, want)
		}
	}
}

func TestAppNameFrom(t *testing.T) {
	for in, want := range map[string]string{
		"/x/My App":   "my-app",
		"/x/api_v2":   "api-v2",
		"/x/---":      "app",
		"/x/Проект":   "app",
		"/x/hello.io": "hello-io",
	} {
		if got := appNameFrom(in); got != want {
			t.Errorf("appNameFrom(%q) = %q, want %q", in, got, want)
		}
	}
}
