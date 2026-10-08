package cli

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/semenov/homebase/internal/config"
	"github.com/semenov/homebase/internal/proto"
)

// Commands that act on the deployed app. "prod" is the app on the server,
// as opposed to the dev server on this Mac.

var (
	flagServer string // -s/--server user@host
	flagProd   bool   // --prod on commands that work on both sides
)

// resolveServer picks the server: --server, [deploy] server in homebase.toml,
// $HOMEBASE_SERVER (or ship's $SHIP_SERVER), or the one from `homebase server add`.
func resolveServer(cfg *config.Config, d *config.Deploy) (string, error) {
	var def string
	if cfg.Remote != nil {
		def = cfg.Remote.Host
	}
	var fromProject string
	if d != nil {
		fromProject = d.Server
	}
	s := firstNonEmpty(flagServer, fromProject, os.Getenv("HOMEBASE_SERVER"), os.Getenv("SHIP_SERVER"), def)
	if s == "" {
		return "", errf(CodeConfig, "add your server once: homebase server add user@host (or pass --server user@host)", "no server yet")
	}
	return s, nil
}

// appName is the app's name on the server: -a, [deploy] name, the project's
// name, the dev server registered for the folder, or the folder name.
func appName(cfg *config.Config, dir string, proj *config.Project) (string, error) {
	name := flagApp
	if name == "" && proj != nil {
		if proj.Deploy != nil {
			name = proj.Deploy.Name
		}
		name = firstNonEmpty(name, proj.Name)
	}
	if name == "" {
		for _, n := range cfg.Names() {
			if cfg.Servers[n].Dir == dir {
				name = n
				break
			}
		}
	}
	if name == "" {
		if name, err := nameFromDir(dir); err != nil || len(name) <= 42 {
			return name, err
		}
		return config.ShipAppName(dir), nil // app names are at most 42 characters
	}
	return name, nil
}

// prodTarget resolves the server and app for commands on the deployed app.
func prodTarget() (*Remote, string, error) {
	cfg, proj, dir, err := currentProject()
	if err != nil {
		return nil, "", err
	}
	name, err := appName(cfg, dir, proj)
	if err != nil {
		return nil, "", err
	}
	var d *config.Deploy
	if proj != nil {
		d = proj.Deploy
	}
	server, err := resolveServer(cfg, d)
	if err != nil {
		return nil, "", err
	}
	r, err := Connect(server)
	if err != nil {
		return nil, "", err
	}
	return r, name, nil
}

// currentProject loads the project of the current folder (nil if it has no
// homebase.toml).
func currentProject() (*config.Config, *config.Project, string, error) {
	cfg, err := loadConfig()
	if err != nil {
		return nil, nil, "", err
	}
	dir, err := projectDir(cfg, "")
	if err != nil {
		return nil, nil, "", err
	}
	proj, err := config.LoadProject(dir)
	if err != nil {
		return nil, nil, "", errf(CodeConfig, "fix the file and try again", "%v", err)
	}
	return cfg, proj, dir, nil
}

// ---- status ---------------------------------------------------------------

func prodStatus() (*Remote, *proto.AppStatus, error) {
	r, app, err := prodTarget()
	if err != nil {
		return nil, nil, err
	}
	var st proto.AppStatus
	if err := r.Agent(nil, &st, "status", "--app", app); err != nil {
		return nil, nil, err
	}
	return r, &st, nil
}

func showProd(r *Remote, st *proto.AppStatus) {
	emit(st, func(u *UI) {
		prodHeadline(u, st)
		prodLines(u, st, r.Target)
		if st.State == "running" && !st.Healthy || st.Restarts > 0 {
			u.Hint("See why with", "homebase logs --prod")
		}
	})
}

func prodHeadline(u *UI, st *proto.AppStatus) {
	switch {
	case st.Current == nil:
		u.Off("%s is not running on the server (no release)", st.Name)
	case st.State == "running" && st.Healthy:
		u.OK("%s is live", st.Name)
	case st.State == "running":
		u.Bad("%s is running but unhealthy: %s doesn't answer", st.Name, st.HealthPath)
	default:
		u.Bad("%s is %s", st.Name, st.State)
	}
}

// prodLines prints the deployed app's details.
func prodLines(u *UI, st *proto.AppStatus, server string) {
	u.KV("URL", st.URL)
	if c := st.Current; c != nil {
		u.KV("Release", c.ID+u.p.dim(" · deployed "+ago(c.DeployedAt)+" ago"))
	}
	if st.Previous != nil {
		u.KV("Previous", st.Previous.ID+u.p.dim(" · homebase rollback"))
	}
	if r := st.Resources; r != nil && st.State == "running" {
		usage := "cpu " + cpuStr(r.CPUPercent) + " · mem " + memStr(r)
		if r.VolumeBytes > 0 {
			usage += " · volumes " + humanBytes(r.VolumeBytes)
		}
		if r.DatabaseBytes > 0 {
			usage += " · database " + humanBytes(r.DatabaseBytes)
		}
		u.KV("Usage", usage)
	}
	for _, v := range st.Volumes {
		u.KV("Volume", v+u.p.dim(" (persistent)"))
	}
	if st.Database != nil {
		u.KV("Database", st.Database.Engine+" "+st.Database.Name+u.p.dim(" · DATABASE_URL, homebase db shell"))
	}
	u.KV("Server", server)
	if st.OOMKills > 0 {
		u.gap()
		u.Bullet(u.p.amber("!"), fmt.Sprintf("Killed %d times for running out of memory since this release; raise `memory` under [deploy] in homebase.toml.", st.OOMKills))
	}
	if st.Restarts > 0 {
		u.gap()
		u.Bullet(u.p.amber("!"), fmt.Sprintf("The container restarted %d times.", st.Restarts))
	}
}

func cpuStr(p float64) string { return fmt.Sprintf("%.1f%%", p) }

// memStr shows memory use, against the limit when the app has one.
func memStr(r *proto.Resources) string {
	if r.MemLimitBytes > 0 {
		return fmt.Sprintf("%s/%s", humanBytes(r.MemBytes), humanBytes(r.MemLimitBytes))
	}
	return humanBytes(r.MemBytes)
}

func ago(t time.Time) string {
	d := time.Since(t)
	switch {
	case d < time.Minute:
		return strconv.Itoa(int(d.Seconds())) + "s"
	case d < time.Hour:
		return strconv.Itoa(int(d.Minutes())) + "m"
	case d < 48*time.Hour:
		return strconv.Itoa(int(d.Hours())) + "h"
	}
	return strconv.Itoa(int(d.Hours()/24)) + "d"
}

// ---- releases -------------------------------------------------------------

// prodRelease runs a zero-downtime release of an image the app already has.
func prodRelease(agentCmd, verb string, timeout time.Duration) error {
	r, app, err := prodTarget()
	if err != nil {
		return err
	}
	var res proto.DeployResult
	if err := r.Agent(nil, &res, agentCmd, "--app", app, "--timeout", timeout.String()); err != nil {
		return err
	}
	emit(res, func(u *UI) {
		u.OK("%s %s on prod", res.App, verb)
		u.KV("URL", res.URL)
		u.KV("Release", res.Release.ID+u.p.dim(" · "+res.Release.Image))
		u.KV("Server", r.Target)
	})
	return nil
}

func rollbackCmd() *cobra.Command {
	var timeout time.Duration
	c := &cobra.Command{
		Use:   "rollback",
		Short: "Switch prod back to the previous release (zero downtime)",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			return prodRelease("rollback", "rolled back", timeout)
		},
	}
	c.Flags().DurationVar(&timeout, "timeout", 90*time.Second, "how long to wait for the app to become healthy")
	return c
}

// restartIfDeployed restarts the app so it picks up env changes. Returns true if it did.
func restartIfDeployed(r *Remote, app string) bool {
	var st proto.AppStatus
	if err := r.Agent(nil, &st, "status", "--app", app); err != nil || st.Current == nil {
		return false
	}
	step("Restarting %s on prod to apply the change", app)
	return r.Agent(nil, nil, "restart", "--app", app, "--timeout", (90*time.Second).String()) == nil
}

func destroyCmd() *cobra.Command {
	var data, yes bool
	c := &cobra.Command{
		Use:   "destroy",
		Short: "Remove the app from the server; its data is kept unless --data",
		Long: `Removes the deployed app's containers, images and route from the server.

Volumes, the database and env vars are kept, so deploying again brings its data back.
With --data they are deleted too (the database gets a final backup in
/var/lib/ship/backups/<app>/ first). The dev server on this Mac is not touched.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			r, app, err := prodTarget()
			if err != nil {
				return err
			}
			if !yes {
				what := "this removes %q from %s (its data is kept)"
				if data {
					what = "this removes %q from %s, including its volumes and database"
				}
				return proto.Errf(proto.CodeConfirm, fmt.Sprintf("run it again with --yes: homebase destroy -a %s%s --yes", app, map[bool]string{true: " --data"}[data]), what, app, r.Target)
			}
			a := []string{"destroy", "--app", app}
			if data {
				a = append(a, "--data")
			}
			var res map[string]any
			if err := r.Agent(nil, &res, a...); err != nil {
				return err
			}
			emit(res, func(u *UI) {
				u.Off("%s is gone from %s", app, r.Target)
				if v, ok := res["kept_volumes"]; ok {
					u.KV("Kept", fmt.Sprintf("volumes %v", v))
				}
				if v, ok := res["kept_database"]; ok {
					u.KV("Kept", fmt.Sprintf("database %v", v))
				}
				if v, ok := res["final_backup"]; ok {
					u.KV("Backup", fmt.Sprint(v))
				}
				if _, ok := res["kept_volumes"]; ok || res["kept_database"] != nil {
					u.Para("A new deploy picks them up again. Delete them with `homebase destroy -a %s --data --yes`.", app)
				}
			})
			return nil
		},
	}
	c.Flags().BoolVarP(&yes, "yes", "y", false, "confirm")
	c.Flags().BoolVar(&data, "data", false, "also delete the volumes, the database and env vars")
	return c
}

// ---- env ------------------------------------------------------------------

func envCmd() *cobra.Command {
	var reveal, noRestart bool
	c := &cobra.Command{
		Use:   "env",
		Short: "Environment variables of the dev server, or with --prod of the deployed app",
		Long: `Without --prod: the [env] table of homebase.toml, which the dev server on this
Mac gets. That file is committed, so it is not for secrets.

With --prod: the deployed app's variables, stored on the server (mode 0600). This is
where secrets go. Changing them restarts the app with zero downtime.

  homebase env [--prod]                list (prod values are hidden unless --reveal)
  homebase env set K=V [K2=V2] [--prod] set, then restart
  homebase env unset K... [--prod]      remove, then restart`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error { return runEnv("list", nil, reveal, noRestart) },
	}
	c.PersistentFlags().BoolVar(&flagProd, "prod", false, "the deployed app's variables, on the server")
	c.Flags().BoolVar(&reveal, "reveal", false, "show prod values")
	ls := &cobra.Command{Use: "ls", Aliases: []string{"list"}, Short: "List the variables", Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error { return runEnv("list", nil, reveal, noRestart) }}
	ls.Flags().BoolVar(&reveal, "reveal", false, "show prod values")
	set := &cobra.Command{Use: "set KEY=VALUE...", Short: "Set variables and restart", Args: cobra.MinimumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error { return runEnv("set", args, reveal, noRestart) }}
	unset := &cobra.Command{Use: "unset KEY...", Short: "Remove variables and restart", Args: cobra.MinimumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error { return runEnv("unset", args, reveal, noRestart) }}
	for _, sc := range []*cobra.Command{set, unset} {
		sc.Flags().BoolVar(&noRestart, "no-restart", false, "don't restart")
	}
	c.AddCommand(ls, set, unset)
	return c
}

func runEnv(op string, args []string, reveal, noRestart bool) error {
	if flagProd {
		return prodEnv(op, args, reveal, noRestart)
	}
	if err := macOnly(); err != nil {
		return err
	}
	cfg, proj, dir, err := currentProject()
	if err != nil {
		return err
	}
	if proj == nil {
		proj = &config.Project{}
	}
	env := proj.Env
	if env == nil {
		env = map[string]string{}
	}
	for _, a := range args {
		k, v, ok := strings.Cut(a, "=")
		switch {
		case op == "set" && (!ok || k == ""):
			return errf(CodeUsage, "", "expected KEY=VALUE, got %q", a)
		case op == "set":
			env[k] = v
		default:
			delete(env, a)
		}
	}
	restarted := false
	if op != "list" {
		if len(env) == 0 {
			env = nil
		}
		proj.Env = env
		if proj.Name == "" {
			if proj.Name, err = appName(cfg, dir, proj); err != nil {
				return err
			}
		}
		if err := proj.Save(dir); err != nil {
			return errf(CodeConfig, "", "write %s: %v", config.ProjectFile, err)
		}
		step("Updated [env] in %s %s", short(filepath.Join(dir, config.ProjectFile)), errPalette.dim("(committed with the project: no secrets here)"))
		if names := serversFor(cfg, dir); len(names) == 1 && !noRestart && stateName(launchdStatus(names[0])) == "running" {
			s := cfg.Servers[names[0]]
			if err := syncFromProject(s); err != nil {
				return err
			}
			if err := saveConfig(cfg); err != nil {
				return err
			}
			step("Restarting %s on this Mac", names[0])
			if err := restartLocal(names[0], s, 60*time.Second); err != nil {
				return err
			}
			restarted = true
		}
	}
	emit(map[string]any{"env": env, "file": filepath.Join(dir, config.ProjectFile), "restarted": restarted}, func(u *UI) {
		u.Head("ENV  " + u.p.dim("this Mac, "+config.ProjectFile))
		listEnv(u, env)
		if op == "list" {
			u.Hint("The deployed app's variables:", "homebase env --prod")
		}
	})
	return nil
}

func listEnv(u *UI, env map[string]string) {
	keys := make([]string, 0, len(env))
	for k := range env {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	if len(keys) == 0 {
		u.Line("    " + u.p.dim("no variables"))
	}
	for _, k := range keys {
		u.Line("    " + k + u.p.dim("=") + u.p.blue(env[k]))
	}
}

func prodEnv(op string, args []string, reveal, noRestart bool) error {
	r, app, err := prodTarget()
	if err != nil {
		return err
	}
	a := []string{"env", op, "--app", app}
	if reveal {
		a = append(a, "--reveal")
	}
	var res struct {
		App       string            `json:"app"`
		Env       map[string]string `json:"env"`
		Restarted bool              `json:"restarted"`
	}
	if err := r.Agent(nil, &res, append(a, args...)...); err != nil {
		return err
	}
	if op != "list" && !noRestart {
		res.Restarted = restartIfDeployed(r, app)
	}
	emit(res, func(u *UI) {
		u.Head("ENV  " + u.p.dim(app+" on "+r.Target))
		listEnv(u, res.Env)
		if op == "list" && !reveal && len(res.Env) > 0 {
			u.Hint("Show the values with", "homebase env --prod --reveal")
		}
	})
	return nil
}

// ---- the server -----------------------------------------------------------

func serverCmd() *cobra.Command {
	c := &cobra.Command{
		Use:   "server",
		Short: "Your server: set it up (`server add`) or see how it is doing",
		Args:  cobra.NoArgs,
		RunE:  func(cmd *cobra.Command, args []string) error { return serverStatus() },
	}
	status := &cobra.Command{
		Use:   "status",
		Short: "The server's domains, apps, shares, memory and disk",
		Args:  cobra.NoArgs,
		RunE:  func(cmd *cobra.Command, args []string) error { return serverStatus() },
	}
	c.AddCommand(serverAddCmd(), status)
	return c
}

func serverAddCmd() *cobra.Command {
	var domain, devDomain, wildcard, tokenFile string
	var install, noDefault bool
	c := &cobra.Command{
		Use:   "add user@host",
		Short: "Set up a server for deploys and shares, and make it the default",
		Long: `Connects over SSH, installs homebase's helper (shipd), checks Docker and the reverse
proxy and remembers the server. Safe to run again.

  --install               install missing Docker and Caddy (Debian/Ubuntu)
  --domain example.com    apps get https://<name>.example.com, shared dev servers
                          https://<name>.dev.example.com. Set up the DNS once, at any
                          provider: a record *.example.com → the server's IP (it covers
                          the dev names too), or also *.dev.example.com.
                          Without a domain: <name>.<ip>.sslip.io, which needs no DNS.
  --dev-domain D          put shares somewhere else than dev.<domain>
  --wildcard cloudflare   optional: one *.example.com certificate through a DNS challenge,
                          so new apps get HTTPS instantly (--dns-token-file FILE)

Caddy is the default proxy (automatic HTTPS). If the server already runs nginx on
:443, homebase uses it instead (with certbot).`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			server := args[0]
			step("Connecting to %s", server)
			r, err := Connect(server)
			if err != nil {
				return err
			}
			a := []string{"setup"}
			if ip := PublicIP(server); ip != "" {
				a = append(a, "--ip", ip)
			}
			if domain != "" {
				a = append(a, "--base-domain", domain)
			}
			if devDomain != "" {
				a = append(a, "--dev-domain", devDomain)
			}
			if install {
				a = append(a, "--install")
			}
			var stdin *strings.Reader
			if wildcard != "" {
				token, err := dnsToken(tokenFile)
				if err != nil {
					return err
				}
				a = append(a, "--wildcard", wildcard)
				stdin = strings.NewReader(token + "\n")
			}
			var si proto.ServerInfo
			if stdin != nil {
				err = r.Agent(stdin, &si, a...)
			} else {
				err = r.Agent(nil, &si, a...)
			}
			if err != nil {
				return err
			}
			cfg, err := loadConfig()
			if err != nil {
				return err
			}
			var shareErr error
			if !noDefault {
				if cfg.Remote == nil {
					cfg.Remote = &config.Remote{}
				}
				moved := cfg.Remote.Host != server || cfg.Remote.DevDomain != si.DevDomain
				cfg.Remote.Host, cfg.Remote.Domain, cfg.Remote.DevDomain = server, si.BaseDomain, si.DevDomain
				// shares follow the server; the tunnel reconnects to it
				if shared := sharedNames(cfg); len(shared) > 0 && moved && runtime.GOOS == "darwin" {
					shareErr = republish(cfg, shared)
				}
				if err := saveConfig(cfg); err != nil {
					return err
				}
			}
			dns := dnsProblems(si)
			emit(map[string]any{"server": si, "default": !noDefault, "dns_problems": dns}, func(u *UI) {
				u.OK("%s is ready", server)
				u.KV("System", fmt.Sprintf("linux/%s · docker %s · %s %s", si.Arch, si.Docker, si.Proxy, si.ProxyVersion))
				apps := "https://<name>." + appsDomain(si)
				if si.WildcardDNS != "" {
					apps += u.p.dim(" (one wildcard certificate)")
				}
				u.KV("Apps", apps)
				if si.DevDomain != "" {
					u.KV("Shares", "https://<name>."+si.DevDomain)
				}
				if si.Proxy == "nginx" {
					u.KV("nginx", "https on "+strings.Join(si.SSLListen, ", ")+u.p.dim(", configs in "+si.NginxDir))
				}
				if len(dns) > 0 {
					u.gap()
					for _, p := range dns {
						u.Bullet(u.p.amber("!"), p)
					}
				}
				if noDefault {
					u.Para("Not made the default: pass --server %s to use it.", server)
				}
				if shareErr != nil {
					u.Warn("Shared servers are not reachable: %v", shareErr)
				}
				u.Hint("Deploy a project with", "cd my-app && homebase deploy")
			})
			return nil
		},
	}
	c.Flags().StringVar(&domain, "domain", "", "domain for apps: <name>.DOMAIN, shares <name>.dev.DOMAIN")
	c.Flags().StringVar(&domain, "base-domain", "", "same as --domain")
	c.Flags().MarkHidden("base-domain")
	c.Flags().StringVar(&devDomain, "dev-domain", "", "domain for shares (default dev.DOMAIN)")
	c.Flags().BoolVar(&install, "install", false, "install missing Docker and Caddy")
	c.Flags().StringVar(&wildcard, "wildcard", "", "one *.DOMAIN certificate via a DNS challenge (provider: cloudflare)")
	c.Flags().StringVar(&tokenFile, "dns-token-file", "", "file with the DNS provider's API token (default: $CLOUDFLARE_API_TOKEN)")
	c.Flags().BoolVar(&noDefault, "no-default", false, "don't make this the default server")
	return c
}

func appsDomain(si proto.ServerInfo) string {
	if si.BaseDomain != "" {
		return si.BaseDomain
	}
	return strings.ReplaceAll(si.PublicIP, ".", "-") + ".sslip.io"
}

// dnsProblems checks that the wildcard records point at the server.
func dnsProblems(si proto.ServerInfo) []string {
	if si.PublicIP == "" {
		return nil
	}
	var out []string
	for _, d := range []string{si.BaseDomain, si.DevDomain} {
		if d == "" || strings.HasSuffix(d, ".sslip.io") {
			continue
		}
		if ips, _ := lookupHost("homebase-check." + d); !contains(ips, si.PublicIP) {
			out = append(out, fmt.Sprintf("*.%s does not point at this server yet: add a DNS record *.%s → %s (type A, not proxied)", d, d, si.PublicIP))
		}
	}
	return out
}

func contains(list []string, s string) bool {
	for _, v := range list {
		if v == s {
			return true
		}
	}
	return false
}

// dnsToken reads the DNS API token from a file or $CLOUDFLARE_API_TOKEN.
func dnsToken(file string) (string, error) {
	if file != "" {
		if strings.HasPrefix(file, "~/") {
			home, _ := os.UserHomeDir()
			file = filepath.Join(home, file[2:])
		}
		b, err := os.ReadFile(file)
		if err != nil {
			return "", errf(CodeConfig, "", "cannot read the token file: %v", err)
		}
		line, _, _ := strings.Cut(strings.TrimSpace(string(b)), "\n")
		// accept both a bare token and KEY=token
		if k, v, ok := strings.Cut(line, "="); ok && !strings.ContainsAny(k, " ") {
			line = v
		}
		return strings.Trim(strings.TrimSpace(line), `"'`), nil
	}
	if t := os.Getenv("CLOUDFLARE_API_TOKEN"); t != "" {
		return t, nil
	}
	return "", errf(CodeConfig, "pass --dns-token-file or set CLOUDFLARE_API_TOKEN", "--wildcard needs a DNS API token")
}

type serverOverview struct {
	Host   string             `json:"host"`
	Server proto.ServerInfo   `json:"server"`
	Apps   []*proto.AppStatus `json:"apps"`
	Shares []*proto.Share     `json:"shares"`
}

func fetchServer(host string) (*serverOverview, error) {
	r, err := Connect(host)
	if err != nil {
		return nil, err
	}
	out := &serverOverview{Host: host}
	if err := r.Agent(nil, out, "list"); err != nil {
		return nil, err
	}
	var sh struct {
		Shares []*proto.Share `json:"shares"`
	}
	if err := r.Agent(nil, &sh, "share", "list"); err == nil {
		out.Shares = sh.Shares
	}
	sort.Slice(out.Apps, func(i, j int) bool { return out.Apps[i].Name < out.Apps[j].Name })
	return out, nil
}

func serverStatus() error {
	cfg, err := loadConfig()
	if err != nil {
		return err
	}
	host, err := resolveServer(cfg, nil)
	if err != nil {
		return err
	}
	o, err := fetchServer(host)
	if err != nil {
		return err
	}
	emit(o, func(u *UI) {
		si := o.Server
		u.OK("%s", host)
		u.KV("System", fmt.Sprintf("linux/%s · docker %s · %s %s", si.Arch, si.Docker, si.Proxy, si.ProxyVersion))
		u.KV("Apps", "https://<name>."+appsDomain(si))
		if si.DevDomain != "" {
			u.KV("Shares", "https://<name>."+si.DevDomain)
		}
		if si.MemTotal > 0 {
			u.KV("Memory", humanBytes(si.MemAvailable)+" free of "+humanBytes(si.MemTotal))
			u.KV("Disk", humanBytes(si.DiskFree)+" free of "+humanBytes(si.DiskTotal))
			u.KV("Load", fmt.Sprintf("%.2f (%d cpu)", si.Load1, si.CPUs))
		}
		appTable(u, "APPS", o.Apps)
		if len(o.Shares) > 0 {
			u.Head("SHARED FROM MACS")
			for _, s := range o.Shares {
				u.Line(fmt.Sprintf("    %s  %s", u.p.blue(s.URL), u.p.dim("from "+s.MacName)))
			}
		}
	})
	return nil
}

// appTable lists deployed apps.
func appTable(u *UI, title string, apps []*proto.AppStatus) {
	u.Head(title)
	if len(apps) == 0 {
		u.Line("    " + u.p.dim("none deployed yet: homebase deploy in a project folder"))
		return
	}
	nameW, urlW := 0, 0
	for _, a := range apps {
		nameW, urlW = max(nameW, len(a.Name)), max(urlW, len(a.URL))
	}
	for _, a := range apps {
		mark, detail := u.p.green("●"), ""
		switch {
		case a.Current == nil:
			mark = u.p.dim("○")
			detail = "no release"
		case a.State != "running":
			mark = u.p.red("✗")
			detail = a.State
		case !a.Healthy:
			mark = u.p.red("✗")
			detail = "unhealthy"
		}
		if r := a.Resources; r != nil && a.State == "running" {
			detail = strings.TrimPrefix(detail+" · cpu "+cpuStr(r.CPUPercent)+" · "+memStr(r), " · ")
		}
		if a.OOMKills > 0 {
			detail += fmt.Sprintf(" · out of memory ×%d", a.OOMKills)
		}
		if a.Current != nil {
			detail += " · " + ago(a.Current.DeployedAt) + " ago"
		}
		u.Line(fmt.Sprintf("    %s %s%s  %s%s  %s", mark, a.Name, strings.Repeat(" ", nameW-len(a.Name)),
			u.p.blue(a.URL), strings.Repeat(" ", urlW-len(a.URL)), u.p.dim(strings.TrimPrefix(detail, " · "))))
	}
}
