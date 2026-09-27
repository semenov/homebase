package agent

import "github.com/semenov/ship/internal/proto"

const (
	proxyCaddy = "caddy"
	proxyNginx = "nginx"
)

// route points the app's domain at hostPort through the server's reverse proxy
// and sets a.URL and a.TLS. TLS problems are returned as warnings.
func route(cfg *serverConfig, a *proto.App, hostPort int) ([]string, error) {
	if cfg.proxy() == proxyCaddy {
		return caddyRoute(cfg, a, hostPort)
	}
	return nginxRoute(cfg, a, hostPort)
}

func unroute(cfg *serverConfig, a *proto.App) error {
	if cfg.proxy() == proxyCaddy {
		return caddyUnroute(a)
	}
	return nginxUnroute(cfg, a)
}

// reroute re-applies the routes of all apps, e.g. after switching proxies.
func reroute(cfg *serverConfig) {
	apps, _ := listApps()
	for _, a := range apps {
		if a.Current == nil {
			continue
		}
		progress("Re-routing %s", a.Name)
		if _, err := route(cfg, a, a.Current.HostPort); err != nil {
			progress("  failed: %v", err)
			continue
		}
		saveApp(a)
	}
}
