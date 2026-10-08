package config

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/BurntSushi/toml"
)

// ProjectFile is the per-project settings file, meant to be committed.
const ProjectFile = "homebase.toml"

// ShipFile is the settings file of ship, which homebase replaced. Its settings
// move into the [deploy] table of homebase.toml.
const ShipFile = "ship.toml"

// Project is homebase.toml. Every field is optional. Ports are machine
// specific and normally live in the registry; Port here pins one.
type Project struct {
	Name   string            `toml:"name,omitempty"`
	Start  string            `toml:"start,omitempty"`
	Port   int               `toml:"port,omitzero"`
	Env    map[string]string `toml:"env,omitempty"`
	Deploy *Deploy           `toml:"deploy,omitempty"`

	// FromShip is set when the deploy settings were read from ship.toml;
	// Save moves them into homebase.toml and removes ship.toml.
	FromShip bool `toml:"-"`
}

// Deploy is the [deploy] table: how the project runs on the server.
type Deploy struct {
	Name       string   `toml:"name,omitempty"`   // app name on the server, if not the project's name
	Server     string   `toml:"server,omitempty"` // ssh target, user@host
	Domain     string   `toml:"domain,omitempty"`
	Port       int      `toml:"port,omitzero"`    // port the app listens on in the container
	Health     string   `toml:"health,omitempty"` // must answer non-5xx before traffic switches
	Start      string   `toml:"start,omitempty"`  // start command when there is no Dockerfile
	Dockerfile string   `toml:"dockerfile,omitempty"`
	Build      string   `toml:"build,omitempty"`   // "local" (default) or "remote"
	Volumes    []string `toml:"volumes,omitempty"` // container paths that persist across deploys
	Release    string   `toml:"release,omitempty"` // runs in the new image before traffic switches
	Memory     string   `toml:"memory,omitempty"`  // memory limit like "256m"
}

func (d *Deploy) empty() bool {
	return d.Name == "" && d.Server == "" && d.Domain == "" && d.Port == 0 && d.Health == "" && d.Start == "" &&
		d.Dockerfile == "" && d.Build == "" && len(d.Volumes) == 0 && d.Release == "" && d.Memory == ""
}

// shipProject is the old ship.toml: the deploy settings plus the app name.
type shipProject struct {
	Name string `toml:"name"`
	Deploy
}

// LoadProject reads dir/homebase.toml, taking deploy settings from an old
// ship.toml when it has none. It returns nil, nil if there is neither.
func LoadProject(dir string) (*Project, error) {
	p, err := decode[Project](filepath.Join(dir, ProjectFile))
	if err != nil || p != nil && p.Deploy != nil {
		return p, err
	}
	s, err := decode[shipProject](filepath.Join(dir, ShipFile))
	if err != nil || s == nil {
		return p, err
	}
	if p == nil {
		p = &Project{Name: s.Name}
	}
	p.Deploy, p.FromShip = &s.Deploy, true
	if s.Name != "" && s.Name != p.Name {
		p.Deploy.Name = s.Name
	}
	return p, nil
}

func decode[T any](path string) (*T, error) {
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	v := new(T)
	if _, err := toml.Decode(string(data), v); err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	return v, nil
}

const projectHeader = `# Settings for homebase: run ` + "`homebase`" + ` here to start the dev server
# (it gets its port in $PORT), ` + "`homebase deploy`" + ` to deploy. https://github.com/semenov/homebase
`

func (p *Project) Save(dir string) error {
	var b bytes.Buffer
	b.WriteString(projectHeader + "\n")
	if p.Deploy != nil && p.Deploy.empty() {
		p.Deploy = nil
	}
	if err := toml.NewEncoder(&b).Encode(p); err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(dir, ProjectFile), b.Bytes(), 0o644); err != nil {
		return err
	}
	if p.FromShip {
		p.FromShip = false
		if err := os.Remove(filepath.Join(dir, ShipFile)); err != nil && !errors.Is(err, os.ErrNotExist) {
			return err
		}
	}
	return nil
}

// FindProjectDir returns the nearest directory from dir upwards that has a
// homebase.toml (or an old ship.toml), or "".
func FindProjectDir(dir string) string {
	for d := dir; ; d = filepath.Dir(d) {
		for _, f := range []string{ProjectFile, ShipFile} {
			if _, err := os.Stat(filepath.Join(d, f)); err == nil {
				return d
			}
		}
		if filepath.Dir(d) == d {
			return ""
		}
	}
}
