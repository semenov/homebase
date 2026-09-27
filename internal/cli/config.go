package cli

import (
	"errors"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/BurntSushi/toml"

	"github.com/semenov/ship/internal/proto"
)

const projectFile = "ship.toml"

// Project is ship.toml in the project root. Every field is optional.
type Project struct {
	Name       string   `toml:"name,omitempty"`
	Server     string   `toml:"server,omitempty"`
	Domain     string   `toml:"domain,omitempty"`
	Port       int      `toml:"port,omitzero"`
	Health     string   `toml:"health,omitempty"`
	Start      string   `toml:"start,omitempty"`
	Dockerfile string   `toml:"dockerfile,omitempty"`
	Build      string   `toml:"build,omitempty"`   // "local" (default) or "remote"
	Volumes    []string `toml:"volumes,omitempty"` // container paths that persist across deploys
	Release    string   `toml:"release,omitempty"` // run in the new image before traffic switches, e.g. migrations
	Memory     string   `toml:"memory,omitempty"`  // memory limit like "256m"; the app restarts if it exceeds it
}

func loadProject(dir string) (*Project, bool, error) {
	var p Project
	_, err := toml.DecodeFile(filepath.Join(dir, projectFile), &p)
	if errors.Is(err, os.ErrNotExist) {
		return &p, false, nil
	}
	if err != nil {
		return nil, false, proto.Errf(proto.CodeConfig, "", "invalid %s: %v", projectFile, err)
	}
	return &p, true, nil
}

func saveProject(dir string, p *Project) error {
	f, err := os.Create(filepath.Join(dir, projectFile))
	if err != nil {
		return err
	}
	defer f.Close()
	f.WriteString("# ship deploy config, see `ship docs`\n")
	return toml.NewEncoder(f).Encode(p)
}

// Global is ~/.config/ship/config.toml.
type Global struct {
	DefaultServer string `toml:"default_server,omitempty"`
}

func globalPath() string {
	dir := os.Getenv("XDG_CONFIG_HOME")
	if dir == "" {
		home, _ := os.UserHomeDir()
		dir = filepath.Join(home, ".config")
	}
	return filepath.Join(dir, "ship", "config.toml")
}

func loadGlobal() *Global {
	var g Global
	toml.DecodeFile(globalPath(), &g)
	return &g
}

func saveGlobal(g *Global) error {
	path := globalPath()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	f, err := os.Create(path)
	if err != nil {
		return err
	}
	defer f.Close()
	return toml.NewEncoder(f).Encode(g)
}

var nonName = regexp.MustCompile(`[^a-z0-9-]+`)

// appNameFrom turns a directory name into a valid app name.
func appNameFrom(dir string) string {
	n := nonName.ReplaceAllString(strings.ToLower(filepath.Base(dir)), "-")
	n = strings.Trim(n, "-")
	if len(n) > 42 {
		n = strings.Trim(n[:42], "-")
	}
	if n == "" {
		n = "app"
	}
	return n
}
