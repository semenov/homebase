package agent

import (
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/vsemenov/ship/internal/proto"
)

const (
	portRangeStart = 20000
	portRangeEnd   = 29999
)

func cmdDeploy(args []string) (any, error) {
	fs := newFlags("deploy")
	name := fs.String("app", "", "")
	image := fs.String("image", "", "")
	port := fs.Int("port", 0, "container port")
	domain := fs.String("domain", "", "")
	health := fs.String("health", "", "")
	ip := fs.String("ip", "", "")
	timeout := fs.Duration("timeout", 60*time.Second, "")
	if err := parse(fs, args); err != nil {
		return nil, err
	}
	if err := checkAppName(*name); err != nil {
		return nil, err
	}
	if *image == "" || *port <= 0 {
		return nil, proto.Errf(proto.CodeUsage, "", "deploy needs --image and --port")
	}
	if !imageExists(*image) {
		return nil, proto.Errf(proto.CodeUpload, "", "image %s is not present on the server", *image)
	}
	cfg, err := loadServer(*ip)
	if err != nil {
		return nil, err
	}
	unlock, err := lockApp(*name)
	if err != nil {
		return nil, err
	}
	defer unlock()

	a, err := loadApp(*name)
	var pe *proto.Error
	if errors.As(err, &pe) && pe.Code == proto.CodeNotFound {
		a, err = &proto.App{Name: *name, HealthPath: "/"}, nil
	}
	if err != nil {
		return nil, err
	}
	if *domain != "" {
		a.Domain = strings.ToLower(*domain)
	}
	if a.Domain == "" {
		if a.Domain, err = defaultDomain(cfg, a.Name); err != nil {
			return nil, err
		}
	}
	if *health != "" {
		a.HealthPath = *health
	}
	return release(cfg, a, *image, *port, *timeout)
}

func defaultDomain(cfg *serverConfig, app string) (string, error) {
	if cfg.BaseDomain != "" {
		return app + "." + cfg.BaseDomain, nil
	}
	if ip := net.ParseIP(cfg.PublicIP); ip != nil && ip.To4() != nil {
		return app + "." + strings.ReplaceAll(cfg.PublicIP, ".", "-") + ".sslip.io", nil
	}
	return "", proto.Errf(proto.CodeConfig, "pass --domain, or set a base domain with `ship init <host> --base-domain apps.example.com`", "cannot pick a default domain: server public IP is unknown")
}

// release starts image as a new container next to the current one, waits until
// it answers HTTP, points nginx at it and only then removes the old container.
func release(cfg *serverConfig, a *proto.App, image string, containerPort int, timeout time.Duration) (*proto.DeployResult, error) {
	envFile, err := ensureEnvFile(a.Name)
	if err != nil {
		return nil, err
	}
	hostPort, err := allocPort(a)
	if err != nil {
		return nil, err
	}
	rel := proto.Release{
		ID:            time.Now().UTC().Format("20060102-150405"),
		Image:         image,
		HostPort:      hostPort,
		ContainerPort: containerPort,
		DeployedAt:    time.Now().UTC(),
	}
	rel.Container = "ship-" + a.Name + "-" + rel.ID
	if a.Current != nil && a.Current.Container == rel.Container {
		rel.Container += "b"
	}

	progress("Starting container %s (port %d → %d)", rel.Container, hostPort, containerPort)
	out, err := combined("docker", "run", "-d",
		"--name", rel.Container,
		"--restart", "unless-stopped",
		"--label", "ship.app="+a.Name,
		"--label", "ship.release="+rel.ID,
		"-p", fmt.Sprintf("127.0.0.1:%d:%d", hostPort, containerPort),
		"--env-file", envFile,
		"-e", "PORT="+strconv.Itoa(containerPort),
		"--log-opt", "max-size=10m", "--log-opt", "max-file=3",
		image)
	if err != nil {
		discard(a, rel.Container)
		return nil, &proto.Error{Code: proto.CodeContainer, Message: "docker run failed", Logs: tail(out, 20)}
	}

	progress("Waiting for the app to answer on %s (timeout %s)", a.HealthPath, timeout)
	if err := waitHealthy(rel.Container, hostPort, a.HealthPath, timeout); err != nil {
		err.Logs = containerLogs(rel.Container, 60)
		discard(a, rel.Container)
		return nil, err
	}

	warnings, err := route(cfg, a, hostPort)
	if err != nil {
		if a.Current != nil {
			route(cfg, a, a.Current.HostPort) // point nginx back at the running release
		}
		discard(a, rel.Container)
		return nil, err
	}

	old := a.Current
	if old != nil && old.Image != image {
		a.Previous = old
	}
	a.Current = &rel
	a.UpdatedAt = time.Now().UTC()
	if err := saveApp(a); err != nil {
		return nil, err
	}
	if old != nil {
		progress("Removing previous container %s", old.Container)
		combined("docker", "stop", "-t", "15", old.Container)
		combined("docker", "rm", old.Container)
	}
	pruneImages(a)
	return &proto.DeployResult{App: a.Name, URL: a.URL, Domain: a.Domain, TLS: a.TLS, Release: rel, Warnings: warnings}, nil
}

func waitHealthy(container string, port int, path string, timeout time.Duration) *proto.Error {
	hint := "check the logs below; the app must listen on 0.0.0.0 and the port given by $PORT (or pass --port)"
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		st, err := inspectContainer(container)
		if err != nil {
			return proto.Errf(proto.CodeContainer, hint, "container disappeared: %v", err)
		}
		if st.Status == "exited" || st.Status == "dead" {
			return proto.Errf(proto.CodeContainer, hint, "container exited with code %d", st.ExitCode)
		}
		if st.Restarts > 0 {
			return proto.Errf(proto.CodeContainer, hint, "container crashed on start (restarted %d times)", st.Restarts)
		}
		if probe(port, path) {
			return nil
		}
		time.Sleep(time.Second)
	}
	return proto.Errf(proto.CodeHealth, hint, "app did not answer HTTP on %s within %s", path, timeout)
}

// route points the app's domain at hostPort, obtaining a certificate if needed.
// TLS problems are warnings, not failures: the app stays reachable over http.
func route(cfg *serverConfig, a *proto.App, hostPort int) ([]string, error) {
	path := siteConfPath(cfg, a.Name)
	var warnings []string
	a.TLS = haveCert(a.Domain)
	if !a.TLS {
		progress("Configuring nginx for http://%s", a.Domain)
		if err := applyNginx(path, renderSite(cfg, a.Name, a.Domain, hostPort, false)); err != nil {
			return nil, err
		}
		if ok, why := dnsPointsHere(a.Domain, cfg.PublicIP); !ok {
			warnings = append(warnings, fmt.Sprintf("no TLS: %s; add an A record %s → %s and redeploy", why, a.Domain, cfg.PublicIP))
		} else {
			progress("Requesting TLS certificate for %s", a.Domain)
			if err := obtainCert(a.Domain); err != nil {
				warnings = append(warnings, "no TLS: certbot failed: "+err.Error())
			} else {
				a.TLS, a.CertByShip = true, true
			}
		}
	}
	if a.TLS {
		progress("Configuring nginx for https://%s", a.Domain)
		if err := applyNginx(path, renderSite(cfg, a.Name, a.Domain, hostPort, true)); err != nil {
			return nil, err
		}
	}
	a.URL = "http://" + a.Domain
	if a.TLS {
		a.URL = "https://" + a.Domain
	}
	return warnings, nil
}

func dnsPointsHere(domain, ip string) (bool, string) {
	if ip == "" {
		return true, ""
	}
	addrs, err := net.LookupHost(domain)
	if err != nil || len(addrs) == 0 {
		return false, domain + " does not resolve"
	}
	if !slices.Contains(addrs, ip) {
		return false, fmt.Sprintf("%s resolves to %s, not to this server", domain, strings.Join(addrs, ", "))
	}
	return true, ""
}

func obtainCert(domain string) error {
	if err := os.MkdirAll(acmeWebroot, 0o755); err != nil {
		return err
	}
	args := []string{"certonly", "--webroot", "-w", acmeWebroot, "-d", domain,
		"--non-interactive", "--agree-tos", "--keep-until-expiring",
		"--deploy-hook", "systemctl reload nginx"}
	if entries, _ := os.ReadDir("/etc/letsencrypt/accounts"); len(entries) == 0 {
		args = append(args, "--register-unsafely-without-email")
	}
	out, err := combined("certbot", args...)
	if err != nil {
		return errors.New(tail(out, 5))
	}
	return nil
}

func ensureEnvFile(app string) (string, error) {
	dir := appDir(app)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", err
	}
	path := filepath.Join(dir, "env")
	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDONLY, 0o600)
	if err != nil {
		return "", err
	}
	f.Close()
	return path, nil
}

func allocPort(a *proto.App) (int, error) {
	used := map[int]bool{}
	apps, _ := listApps()
	for _, o := range apps {
		for _, r := range []*proto.Release{o.Current, o.Previous} {
			if r != nil {
				used[r.HostPort] = true
			}
		}
	}
	if a.Current != nil {
		used[a.Current.HostPort] = true
	}
	for p := portRangeStart; p <= portRangeEnd; p++ {
		if !used[p] && portFree(p) {
			return p, nil
		}
	}
	return 0, proto.Errf(proto.CodeContainer, "", "no free ports in %d-%d", portRangeStart, portRangeEnd)
}

// discard removes a release that failed to go live, including its image.
func discard(a *proto.App, container string) {
	combined("docker", "rm", "-f", container)
	pruneImages(a)
}

// pruneImages keeps only the current and previous image of the app.
func pruneImages(a *proto.App) {
	out, err := output("docker", "images", "ship/"+a.Name, "--format", "{{.Repository}}:{{.Tag}}")
	if err != nil {
		return
	}
	keep := map[string]bool{}
	for _, r := range []*proto.Release{a.Current, a.Previous} {
		if r != nil {
			keep[r.Image] = true
		}
	}
	for _, img := range strings.Fields(out) {
		if !keep[img] {
			combined("docker", "rmi", img)
		}
	}
}
