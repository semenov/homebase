package cli

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/spf13/cobra"

	"github.com/semenov/homebase/internal/cloudflared"
	"github.com/semenov/homebase/internal/config"
	"github.com/semenov/homebase/internal/launchd"
	"github.com/semenov/homebase/internal/proxy"
)

func initCmd() *cobra.Command {
	var lan, noLAN, noTunnel bool
	var tunnel string
	c := &cobra.Command{
		Use:   "init",
		Short: "Set up this Mac: the *.localhost proxy, and optionally LAN and tunnel access",
		Long: `Sets up the machine-wide parts of homebase. Safe to run again; flags change
the setup, no flags just make sure everything is running.

  homebase init                      the proxy: http://<name>.localhost (port 80)
  homebase init --lan                also http://<name>.local for phones and other
                                     devices on your network (Bonjour)
  homebase init --tunnel example.com also https://<name>.example.com from anywhere,
                                     for servers you ` + "`homebase share`" + ` (Cloudflare Tunnel)

--no-lan and --no-tunnel turn those off again.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			if lan && noLAN || tunnel != "" && noTunnel {
				return errf(CodeUsage, "", "contradicting flags")
			}
			cfg, err := loadConfig()
			if err != nil {
				return err
			}
			if lan || noLAN {
				cfg.Proxy.LAN = lan
			}
			if noTunnel && cfg.Tunnel != nil {
				step("Removing the tunnel from this Mac")
				if err := launchd.Remove(tunnelLabel); err != nil {
					return errf(CodeLaunchd, "", "%v", err)
				}
				os.Remove(cloudflaredConfigPath())
				cfg.Tunnel = nil
			}
			if tunnel != "" {
				if err := setupTunnel(cfg, strings.ToLower(strings.TrimSuffix(tunnel, "."))); err != nil {
					return err
				}
			}
			if err := saveConfig(cfg); err != nil {
				return err
			}
			if cfg.Tunnel != nil && !launchd.Get(tunnelLabel).Running {
				if err := startTunnel(cfg); err != nil {
					return err
				}
			}
			// (Re)start the proxy last: it reads the LAN and tunnel settings
			// at startup, and this picks up a new homebase binary too.
			step("Starting the proxy")
			if err := installProxy(); err != nil {
				return err
			}

			m := machine(cfg)
			emit(m, func(u *UI) {
				u.OK("This Mac is set up")
				machineLines(u, m)
				if m.LAN.On {
					u.Para("Anyone on your network can open your servers at http://<name>.local.")
				}
				if hint := agentsHint(); hint != "" {
					u.Hint("Tell your coding agents about homebase:", "homebase agents install")
				}
				u.Hint("Start a project with", "cd my-app && homebase")
			})
			return nil
		},
	}
	c.Flags().BoolVar(&lan, "lan", false, "reach servers from other devices at http://<name>.local")
	c.Flags().BoolVar(&noLAN, "no-lan", false, "turn LAN access off")
	c.Flags().StringVar(&tunnel, "tunnel", "", "publish shared servers at https://<name>.DOMAIN through Cloudflare")
	c.Flags().BoolVar(&noTunnel, "no-tunnel", false, "remove the tunnel from this Mac")
	return c
}

func installProxy() error {
	exe, err := stableExecutable()
	if err != nil {
		return err
	}
	job := &launchd.Job{
		Label:     proxyLabel,
		Args:      []string{exe, "proxy", "run"},
		LogPath:   logPath("proxy"),
		KeepAlive: true,
	}
	if p := os.Getenv("HOMEBASE_CONFIG"); p != "" {
		job.Env = map[string]string{"HOMEBASE_CONFIG": p}
	}
	if err := launchd.Start(job); err != nil {
		return errf(CodeLaunchd, "", "start the proxy: %v", err)
	}
	return nil
}

// stableExecutable returns a path to this binary that survives upgrades, for
// the proxy launch agent. Homebrew installs into versioned directories and
// links them from its bin dir, so prefer the `homebase` on PATH when it is a
// link to this same binary.
func stableExecutable() (string, error) {
	exe, err := os.Executable()
	if err != nil {
		return "", err
	}
	if exe, err = filepath.EvalSymlinks(exe); err != nil {
		return "", err
	}
	if strings.Contains(exe, "/go-build") {
		return "", errf(CodeConfig, "brew install semenov/tap/homebase, or go install", "install homebase first: the proxy needs a permanent path to the binary")
	}
	if p, err := exec.LookPath("homebase"); err == nil {
		if p, err = filepath.Abs(p); err == nil {
			if resolved, err := filepath.EvalSymlinks(p); err == nil && resolved == exe {
				return p, nil
			}
		}
	}
	return exe, nil
}

func cloudflaredConfigPath() string {
	return filepath.Join(filepath.Dir(config.Path()), "cloudflared.yml")
}

func setupTunnel(cfg *config.Config, domain string) error {
	if !strings.Contains(domain, ".") {
		return errf(CodeUsage, "", "%q does not look like a domain", domain)
	}
	if _, err := cloudflared.Path(); err != nil {
		return errf(CodeCloudflare, "brew install cloudflared", "cloudflared is not installed")
	}
	if !cloudflared.LoggedIn() {
		return errf(CodeCloudflare, "run `cloudflared tunnel login`, pick "+domain+" in the browser, then run this again",
			"this Mac is not logged in to Cloudflare")
	}
	step("Creating the Cloudflare tunnel %q", tunnelName)
	id, err := cloudflared.EnsureTunnel(tunnelName)
	if err != nil {
		return errf(CodeCloudflare, "", "%v", err)
	}
	if _, err := os.Stat(cloudflared.CredentialsPath(id)); err != nil {
		return errf(CodeCloudflare, "delete it with `cloudflared tunnel delete "+tunnelName+"` and run this again",
			"the tunnel %q exists, but its credentials are not on this Mac", tunnelName)
	}
	port := config.DefaultTunnelPort
	if cfg.Tunnel != nil && cfg.Tunnel.Port != 0 {
		port = cfg.Tunnel.Port
	}
	cfg.Tunnel = &config.Tunnel{Name: tunnelName, ID: id, Domain: domain, Port: port}
	if err := cloudflared.WriteConfig(cloudflaredConfigPath(), id, port); err != nil {
		return err
	}
	return startTunnel(cfg)
}

func startTunnel(cfg *config.Config) error {
	bin, err := cloudflared.Path()
	if err != nil {
		return errf(CodeCloudflare, "brew install cloudflared", "cloudflared is not installed")
	}
	step("Starting the tunnel for *.%s", cfg.Tunnel.Domain)
	err = launchd.Start(&launchd.Job{
		Label:     tunnelLabel,
		Args:      []string{bin, "tunnel", "--no-autoupdate", "--config", cloudflaredConfigPath(), "run"},
		LogPath:   logPath("tunnel"),
		KeepAlive: true,
	})
	if err != nil {
		return errf(CodeLaunchd, "", "start the tunnel: %v", err)
	}
	return nil
}

func uninstallCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "uninstall",
		Short: "Stop everything homebase runs on this Mac (settings are kept)",
		Long: `Stops and removes every launch agent homebase created: all servers, the proxy
and the tunnel. Settings and homebase.toml files stay, so ` + "`homebase init`" + ` and
` + "`homebase`" + ` in each project bring everything back. Run this before ` + "`brew uninstall homebase`" + `.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, err := loadConfig()
			if err != nil {
				return err
			}
			labels := []string{proxyLabel, tunnelLabel}
			for _, n := range cfg.Names() {
				labels = append(labels, serverLabelPrefix+n)
			}
			var errs []error
			for _, l := range labels {
				errs = append(errs, launchd.Remove(l))
			}
			if err := errors.Join(errs...); err != nil {
				return errf(CodeLaunchd, "", "%v", err)
			}
			emit(map[string]any{"stopped": len(cfg.Servers)}, func(u *UI) {
				u.Off("homebase stopped everything on this Mac")
				u.Para("Removed the launch agents of %d servers, the proxy and the tunnel. Your settings in %s "+
					"and the projects' homebase.toml files are kept.", len(cfg.Servers), short(filepath.Dir(config.Path())))
				u.Hint("Bring it back with", "homebase init")
			})
			return nil
		},
	}
}

// proxyCmd is what the proxy launch agent runs; not for people.
func proxyCmd() *cobra.Command {
	c := &cobra.Command{Use: "proxy", Hidden: true}
	c.AddCommand(&cobra.Command{
		Use:  "run",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error { return proxy.Run() },
	})
	return c
}
