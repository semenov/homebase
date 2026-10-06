package proxy

import (
	"crypto/subtle"
	"net"
	"net/http"
	"net/http/httputil"
	"strings"

	"github.com/semenov/homebase/internal/config"
)

// TokenParam and TokenCookie carry the secret of a private share: the link
// contains ?homebase_token=..., which is swapped for a cookie on first visit.
// Scripts can send the X-Homebase-Token header instead.
const (
	TokenParam  = "homebase_token"
	TokenCookie = "homebase_token"
	tokenHeader = "X-Homebase-Token"
)

// serveTunnel handles traffic from cloudflared. It listens on loopback only,
// so every request here came through Cloudflare from the internet.
func (p *Proxy) serveTunnel(w http.ResponseWriter, r *http.Request) {
	cfg := p.config()
	host := strings.ToLower(r.Host)
	if h, _, err := net.SplitHostPort(host); err == nil {
		host = h
	}
	var name string
	var srv *config.Server
	if cfg.Tunnel != nil {
		if n, ok := strings.CutSuffix(host, "."+cfg.Tunnel.Domain); ok {
			name, srv = n, cfg.Servers[n]
		}
	}
	if srv == nil || srv.Share == nil {
		http.Error(w, "Not found", http.StatusNotFound)
		return
	}

	if !srv.Share.Public {
		if q := r.URL.Query(); q.Has(TokenParam) {
			if !validToken(srv.Share.Token, q.Get(TokenParam)) {
				deny(w)
				return
			}
			http.SetCookie(w, &http.Cookie{
				Name: TokenCookie, Value: srv.Share.Token, Path: "/",
				MaxAge: 365 * 24 * 3600, Secure: true, HttpOnly: true, SameSite: http.SameSiteLaxMode,
			})
			q.Del(TokenParam)
			u := *r.URL
			u.RawQuery = q.Encode()
			http.Redirect(w, r, u.RequestURI(), http.StatusFound)
			return
		}
		got := r.Header.Get(tokenHeader)
		if c, err := r.Cookie(TokenCookie); err == nil && got == "" {
			got = c.Value
		}
		if !validToken(srv.Share.Token, got) {
			deny(w)
			return
		}
	}

	reverseProxy(name, srv, func(pr *httputil.ProxyRequest) {
		// Dev servers like Vite reject unknown Host headers but accept
		// *.localhost, so present the local name and pass the public one in
		// X-Forwarded-Host.
		pr.Out.Host = name + ".localhost"
		pr.Out.Header.Set("X-Forwarded-Host", pr.In.Host)
		pr.Out.Header.Set("X-Forwarded-Proto", "https")
		if ip := pr.In.Header.Get("Cf-Connecting-Ip"); ip != "" {
			pr.Out.Header.Set("X-Forwarded-For", ip)
		}
		pr.Out.Header.Del(tokenHeader)
		stripCookie(pr.Out, TokenCookie)
	}).ServeHTTP(w, r)
}

func validToken(want, got string) bool {
	return want != "" && subtle.ConstantTimeCompare([]byte(want), []byte(got)) == 1
}

func deny(w http.ResponseWriter) {
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.WriteHeader(http.StatusUnauthorized)
	w.Write([]byte("This server is private. Open it with the link from `homebase share`.\n"))
}

// stripCookie removes one cookie so the share token never reaches the app.
func stripCookie(r *http.Request, name string) {
	cookies := r.Cookies()
	r.Header.Del("Cookie")
	for _, c := range cookies {
		if c.Name != name {
			r.AddCookie(c)
		}
	}
}
