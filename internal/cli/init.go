package cli

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/semenov/homebase/internal/config"
	"github.com/semenov/homebase/internal/launchd"
	"github.com/semenov/homebase/internal/proxy"
)

func initCmd() *cobra.Command {
	var lan, noLAN, noTunnel bool
	var tunnel string
	c := &cobra.Command{
		Use:   "init",
		Short: "Set up this Mac: the *.localhost proxy, and optionally LAN access",
		Long: `Sets up the machine-wide parts of homebase. Safe to run again; flags change
the setup, no flags just make sure everything is running (and pick up a new
homebase binary).

  homebase init          the proxy: http://<name>.localhost (port 80)
  homebase init --lan    also http://<name>.local for phones and other devices
                         on your network (Bonjour)

--no-lan turns LAN access off again. Public URLs for shared servers come from
your own server: see ` + "`homebase server add`" + `.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			if tunnel != "" || noTunnel {
				return errf(CodeUsage, "share through your own server instead: homebase server add user@host --domain example.com",
					"--tunnel is gone: homebase no longer uses Cloudflare tunnels")
			}
			if lan && noLAN {
				return errf(CodeUsage, "", "contradicting flags")
			}
			if err := macOnly(); err != nil {
				return err
			}
			cfg, err := loadConfig()
			if err != nil {
				return err
			}
			if lan || noLAN {
				cfg.Proxy.LAN = lan
			}
			legacy := dropLegacyTunnel(cfg)
			// (Re)start the proxy: it reads the LAN setting at startup, and
			// this picks up a new homebase binary too.
			step("Starting the proxy")
			if err := installProxy(); err != nil {
				return err
			}
			var shareErr error
			if shared := sharedNames(cfg); len(shared) > 0 {
				shareErr = republish(cfg, shared, legacy != nil)
			}
			if err := saveConfig(cfg); err != nil {
				return err
			}

			m := machine(cfg)
			emit(m, func(u *UI) {
				u.OK("This Mac is set up")
				machineLines(u, m)
				if m.LAN.On {
					u.Para("Anyone on your network can open your servers at http://<name>.local.")
				}
				if shareErr != nil {
					u.Warn("Shared servers are not reachable: %v", shareErr)
				}
				legacyNotice(u, legacy)
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
	c.Flags().StringVar(&tunnel, "tunnel", "", "")
	c.Flags().BoolVar(&noTunnel, "no-tunnel", false, "")
	c.Flags().MarkHidden("tunnel")
	c.Flags().MarkHidden("no-tunnel")
	return c
}

// republish makes sure shared servers are reachable: their routes exist on
// the server (after a move from the Cloudflare tunnel) and the tunnel runs.
// Without a server they can't be, so they are unshared.
func republish(cfg *config.Config, names []string, routes bool) error {
	if cfg.Remote == nil {
		for _, n := range names {
			cfg.Servers[n].Share = nil
		}
		return errf(CodeConfig, "", "there is no server to share through (homebase server add user@host), so %s are no longer shared", strings.Join(names, ", "))
	}
	ensureMacID(cfg)
	if routes || cfg.Remote.DevDomain == "" {
		step("Publishing %s through %s", strings.Join(names, ", "), cfg.Remote.Host)
		r, err := Connect(cfg.Remote.Host)
		if err != nil {
			return err
		}
		for _, n := range names {
			if _, err := publish(r, cfg, n); err != nil {
				return err
			}
		}
	}
	return ensureTunnel()
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
	for i := 0; i < 30 && !launchd.Get(proxyLabel).Running; i++ {
		time.Sleep(100 * time.Millisecond)
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

func uninstallCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "uninstall",
		Short: "Stop everything homebase runs on this Mac (settings are kept)",
		Long: `Stops and removes every launch agent homebase created on this Mac: all dev
servers, the proxy and the tunnel. Settings and homebase.toml files stay, so
` + "`homebase init`" + ` and ` + "`homebase`" + ` in each project bring everything back. Apps deployed
to your server keep running. Run this before ` + "`brew uninstall homebase`" + `.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := macOnly(); err != nil {
				return err
			}
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
