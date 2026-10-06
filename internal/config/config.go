// Package config loads and saves the homebase server registry
// (~/.config/homebase/servers.yaml by default, or $HOMEBASE_CONFIG).
package config

import (
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"time"

	"gopkg.in/yaml.v3"
)

const (
	DefaultProxyPort = 80
	DefaultDomain    = "localhost"
	// DefaultTunnelPort is the loopback port where the proxy receives
	// traffic from cloudflared.
	DefaultTunnelPort = 8780
	firstAutoPort     = 4000
)

type Proxy struct {
	Port   int    `yaml:"port"`
	Domain string `yaml:"domain"`
	// LAN announces <name>.local over Bonjour and lets other devices on the
	// network use the proxy. Off by default: the proxy listens on all
	// interfaces (macOS only allows :80 without root that way), so without
	// this it rejects non-loopback clients.
	LAN bool `yaml:"lan,omitempty"`
}

// Tunnel is the optional Cloudflare Tunnel that publishes shared servers
// at https://<name>.<Domain>.
type Tunnel struct {
	Name   string `yaml:"name"`
	ID     string `yaml:"id"`
	Domain string `yaml:"domain"`
	Port   int    `yaml:"port"`
}

// Share marks a server as published through the tunnel. Private shares
// (the default) only open with Token.
type Share struct {
	Public bool   `yaml:"public,omitempty"`
	Token  string `yaml:"token,omitempty"`
}

type Server struct {
	Dir     string            `yaml:"dir"`
	Command string            `yaml:"command"`
	Port    int               `yaml:"port"`
	Env     map[string]string `yaml:"env,omitempty"`
	Share   *Share            `yaml:"share,omitempty"`
}

type Config struct {
	Proxy   Proxy              `yaml:"proxy"`
	Tunnel  *Tunnel            `yaml:"tunnel,omitempty"`
	Servers map[string]*Server `yaml:"servers"`
}

var nameRe = regexp.MustCompile(`^[a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?$`)

// ValidName reports whether name can be used as a server name. Names become
// DNS labels (<name>.localhost) and launchd labels, so they are kept strict.
func ValidName(name string) error {
	if !nameRe.MatchString(name) {
		return fmt.Errorf("invalid name %q: use lowercase letters, digits and dashes", name)
	}
	if name == "proxy" || name == "tunnel" {
		return fmt.Errorf("%q is reserved for homebase's own logs", name)
	}
	return nil
}

func Path() string {
	if p := os.Getenv("HOMEBASE_CONFIG"); p != "" {
		return p
	}
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".config", "homebase", "servers.yaml")
}

// Load reads the config, returning defaults if the file does not exist yet.
func Load() (*Config, error) {
	c := &Config{}
	data, err := os.ReadFile(Path())
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return nil, err
	}
	if err := yaml.Unmarshal(data, c); err != nil {
		return nil, fmt.Errorf("parse %s: %w", Path(), err)
	}
	if c.Proxy.Port == 0 {
		c.Proxy.Port = DefaultProxyPort
	}
	if c.Proxy.Domain == "" {
		c.Proxy.Domain = DefaultDomain
	}
	if c.Servers == nil {
		c.Servers = map[string]*Server{}
	}
	if c.Tunnel != nil && c.Tunnel.Port == 0 {
		c.Tunnel.Port = DefaultTunnelPort
	}
	return c, nil
}

func (c *Config) Save() error {
	data, err := yaml.Marshal(c)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(Path()), 0o755); err != nil {
		return err
	}
	tmp := Path() + ".tmp"
	// 0600: the file holds share tokens.
	if err := os.WriteFile(tmp, data, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, Path())
}

func (c *Config) Names() []string {
	names := make([]string, 0, len(c.Servers))
	for n := range c.Servers {
		names = append(names, n)
	}
	sort.Strings(names)
	return names
}

func (c *Config) Get(name string) (*Server, error) {
	s, ok := c.Servers[name]
	if !ok {
		return nil, fmt.Errorf("no server named %q (see `homebase ls`)", name)
	}
	return s, nil
}

// FreePort returns the lowest port >= 4000 that is neither assigned to
// another server nor currently accepting connections.
func (c *Config) FreePort() int {
	used := map[int]bool{c.Proxy.Port: true, DefaultTunnelPort: true}
	for _, s := range c.Servers {
		used[s.Port] = true
	}
	for p := firstAutoPort; ; p++ {
		if !used[p] && !Listening(p) {
			return p
		}
	}
}

// URL is the proxied address of a server, e.g. http://api.localhost.
func (c *Config) URL(name string) string { return c.url(name + "." + c.Proxy.Domain) }

// LANURL is the address other devices use, e.g. http://api.local.
func (c *Config) LANURL(name string) string { return c.url(name + ".local") }

// PublicURL is the tunnel address of a server, or "" without a tunnel.
func (c *Config) PublicURL(name string) string {
	if c.Tunnel == nil {
		return ""
	}
	return "https://" + name + "." + c.Tunnel.Domain
}

func (c *Config) url(host string) string {
	if c.Proxy.Port != 80 {
		host += ":" + strconv.Itoa(c.Proxy.Port)
	}
	return "http://" + host
}

// Listening reports whether something accepts TCP connections on localhost:port.
func Listening(port int) bool {
	for _, host := range []string{"127.0.0.1", "::1"} {
		conn, err := net.DialTimeout("tcp", net.JoinHostPort(host, strconv.Itoa(port)), 200*time.Millisecond)
		if err == nil {
			conn.Close()
			return true
		}
	}
	return false
}
