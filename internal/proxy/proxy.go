// Package proxy routes <name>.<domain> requests to the server's local port.
// The config file is re-read when it changes, so adding a server never
// requires restarting the proxy.
package proxy

import (
	"fmt"
	"html/template"
	"log"
	"net"
	"net/http"
	"net/http/httputil"
	"net/url"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/semenov/homebase/internal/bonjour"
	"github.com/semenov/homebase/internal/config"
)

type Proxy struct {
	mu      sync.Mutex
	cfg     *config.Config
	modTime time.Time
}

func (p *Proxy) config() *config.Config {
	p.mu.Lock()
	defer p.mu.Unlock()
	var mt time.Time
	if fi, err := os.Stat(config.Path()); err == nil {
		mt = fi.ModTime()
	}
	if p.cfg == nil || !mt.Equal(p.modTime) {
		cfg, err := config.Load()
		if err != nil {
			log.Printf("reload config: %v", err)
			if p.cfg != nil {
				return p.cfg
			}
			cfg = &config.Config{Servers: map[string]*config.Server{}}
		}
		p.cfg, p.modTime = cfg, mt
	}
	return p.cfg
}

// Run listens on the configured port (read once at startup) and, while LAN
// mode is on, keeps <name>.local announced over Bonjour.
func Run() error {
	p := &Proxy{}
	cfg := p.config()
	addr := ":" + strconv.Itoa(cfg.Proxy.Port)
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		return err
	}
	log.Printf("homebase proxy listening on %s (*.%s)", addr, cfg.Proxy.Domain)

	if cfg.Tunnel != nil {
		taddr := net.JoinHostPort("127.0.0.1", strconv.Itoa(cfg.Tunnel.Port))
		tln, err := net.Listen("tcp", taddr)
		if err != nil {
			return fmt.Errorf("tunnel listener: %w", err)
		}
		log.Printf("tunnel listener on %s (*.%s)", taddr, cfg.Tunnel.Domain)
		go func() { log.Fatal(http.Serve(tln, http.HandlerFunc(p.serveTunnel))) }()
	}

	pub := bonjour.NewPublisher()
	go p.announce(pub)
	sig := make(chan os.Signal, 1)
	signal.Notify(sig, syscall.SIGTERM, syscall.SIGINT)
	go func() {
		<-sig
		pub.Close()
		os.Exit(0)
	}()
	return http.Serve(ln, p)
}

func (p *Proxy) announce(pub *bonjour.Publisher) {
	var lastIP string
	for ; ; time.Sleep(5 * time.Second) {
		cfg := p.config()
		var records []bonjour.Record
		if cfg.Proxy.LAN {
			ip, err := bonjour.LANAddr()
			if err != nil {
				if lastIP != "none" {
					log.Printf("bonjour: %v", err)
					lastIP = "none"
				}
			} else {
				if ip.String() != lastIP {
					log.Printf("bonjour: announcing *.local at %s", ip)
					lastIP = ip.String()
				}
				for _, name := range cfg.Names() {
					records = append(records, bonjour.Record{Name: name, IP: ip, Port: cfg.Proxy.Port})
				}
			}
		}
		for _, err := range pub.Sync(records) {
			log.Print(err)
		}
	}
}

func (p *Proxy) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	cfg := p.config()
	if !cfg.Proxy.LAN && !isLoopback(r.RemoteAddr) {
		http.Error(w, "homebase: LAN access is off (homebase lan on)", http.StatusForbidden)
		return
	}

	host := strings.ToLower(r.Host)
	if h, _, err := net.SplitHostPort(host); err == nil {
		host = h
	}
	name, ok := strings.CutSuffix(host, "."+cfg.Proxy.Domain)
	lan := false
	if !ok && cfg.Proxy.LAN {
		name, ok = strings.CutSuffix(host, ".local")
		lan = ok
	}
	// Subdomains map to the same server: app.api.localhost -> api.
	if i := strings.LastIndexByte(name, '.'); i >= 0 {
		name = name[i+1:]
	}
	srv := cfg.Servers[name]
	if !ok || srv == nil {
		w.WriteHeader(http.StatusNotFound)
		p.index(w, cfg, lan || !isLoopback(r.RemoteAddr))
		return
	}

	// Keep the original Host: dev servers (Vite etc.) check it and generate
	// absolute URLs from it.
	reverseProxy(name, srv, func(pr *httputil.ProxyRequest) { pr.Out.Host = pr.In.Host }).ServeHTTP(w, r)
}

// reverseProxy forwards to the server's port; rewrite runs after the
// standard target and X-Forwarded-* setup.
func reverseProxy(name string, srv *config.Server, rewrite func(*httputil.ProxyRequest)) *httputil.ReverseProxy {
	target := &url.URL{Scheme: "http", Host: "localhost:" + strconv.Itoa(srv.Port)}
	return &httputil.ReverseProxy{
		Rewrite: func(pr *httputil.ProxyRequest) {
			pr.SetURL(target)
			pr.SetXForwarded()
			rewrite(pr)
		},
		ErrorHandler: func(w http.ResponseWriter, r *http.Request, err error) {
			w.Header().Set("Content-Type", "text/plain; charset=utf-8")
			w.WriteHeader(http.StatusBadGateway)
			fmt.Fprintf(w, "homebase: %q is not responding on port %d.\n\nStart it:  homebase start %s\nLogs:      homebase logs %s\n\n(%v)\n",
				name, srv.Port, name, name, err)
		},
	}
}

func isLoopback(remoteAddr string) bool {
	host, _, err := net.SplitHostPort(remoteAddr)
	if err != nil {
		return false
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

var indexTmpl = template.Must(template.New("index").Parse(`<!doctype html>
<meta charset="utf-8"><title>homebase</title>
<style>body{font:15px -apple-system,sans-serif;margin:40px}td{padding:4px 16px 4px 0}</style>
<h1>homebase</h1>
{{if .}}<table>{{range .}}<tr><td><a href="{{.URL}}">{{.Name}}</a></td><td>:{{.Port}}</td><td>{{.Command}}</td></tr>{{end}}</table>
{{else}}<p>No servers yet. Add one with <code>homebase add</code>.</p>{{end}}
`))

func (p *Proxy) index(w http.ResponseWriter, cfg *config.Config, lan bool) {
	type row struct {
		Name, URL, Command string
		Port               int
	}
	var rows []row
	for _, n := range cfg.Names() {
		s := cfg.Servers[n]
		url := cfg.URL(n)
		if lan {
			url = cfg.LANURL(n)
		}
		rows = append(rows, row{n, url, s.Command, s.Port})
	}
	indexTmpl.Execute(w, rows)
}
