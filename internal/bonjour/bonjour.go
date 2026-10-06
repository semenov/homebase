// Package bonjour announces <name>.local hostnames on the LAN through the
// system mDNSResponder, using one `dns-sd -P` process per name.
package bonjour

import (
	"fmt"
	"net"
	"os/exec"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
)

// Record is a host to announce: <Name>.local → IP, plus an _http._tcp
// service on Port so it also shows up in Bonjour browsers.
type Record struct {
	Name string
	IP   net.IP
	Port int
}

func (r Record) key() string { return r.Name + " " + r.IP.String() + " " + strconv.Itoa(r.Port) }

type proc struct {
	key  string
	cmd  *exec.Cmd
	done chan struct{}
}

type Publisher struct {
	mu    sync.Mutex
	procs map[string]*proc
}

func NewPublisher() *Publisher { return &Publisher{procs: map[string]*proc{}} }

// Sync makes the announced set equal to records, restarting announcements
// whose IP/port changed or whose dns-sd process died.
func (p *Publisher) Sync(records []Record) []error {
	p.mu.Lock()
	defer p.mu.Unlock()
	want := map[string]Record{}
	for _, r := range records {
		want[r.Name] = r
	}
	for name, pr := range p.procs {
		r, ok := want[name]
		if !ok || r.key() != pr.key || exited(pr) {
			stop(pr)
			delete(p.procs, name)
		}
	}
	var errs []error
	for name, r := range want {
		if _, ok := p.procs[name]; ok {
			continue
		}
		cmd := exec.Command("dns-sd", "-P", r.Name, "_http._tcp", "local",
			strconv.Itoa(r.Port), r.Name+".local", r.IP.String())
		if err := cmd.Start(); err != nil {
			errs = append(errs, fmt.Errorf("announce %s.local: %w", r.Name, err))
			continue
		}
		pr := &proc{key: r.key(), cmd: cmd, done: make(chan struct{})}
		go func() { cmd.Wait(); close(pr.done) }()
		p.procs[name] = pr
	}
	return errs
}

func (p *Publisher) Close() { p.Sync(nil) }

func exited(pr *proc) bool {
	select {
	case <-pr.done:
		return true
	default:
		return false
	}
}

func stop(pr *proc) {
	pr.cmd.Process.Kill()
	<-pr.done
}

var routeIfRe = regexp.MustCompile(`(?m)^\s*interface: (\S+)`)

// LANAddr returns the Mac's IPv4 address on the local network. It prefers
// the default-route interface but only physical ones (en*): with a VPN up
// the default route points at a utun tunnel the phone can't reach.
func LANAddr() (net.IP, error) {
	ifaces, err := net.Interfaces()
	if err != nil {
		return nil, err
	}
	var preferred string
	if out, err := exec.Command("route", "-n", "get", "default").Output(); err == nil {
		if m := routeIfRe.FindSubmatch(out); m != nil {
			preferred = string(m[1])
		}
	}
	sort.SliceStable(ifaces, func(i, j int) bool {
		return ifaces[i].Name == preferred && ifaces[j].Name != preferred
	})
	for _, ifc := range ifaces {
		if !strings.HasPrefix(ifc.Name, "en") || ifc.Flags&net.FlagUp == 0 || ifc.Flags&net.FlagRunning == 0 {
			continue
		}
		addrs, _ := ifc.Addrs()
		for _, a := range addrs {
			if ipn, ok := a.(*net.IPNet); ok {
				if ip4 := ipn.IP.To4(); ip4 != nil && ip4.IsPrivate() {
					return ip4, nil
				}
			}
		}
	}
	return nil, fmt.Errorf("no Wi-Fi/Ethernet IPv4 address found")
}
