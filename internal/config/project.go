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

// Project is homebase.toml. Every field is optional. Ports are machine
// specific and normally live in the registry; Port here pins one.
type Project struct {
	Name  string            `toml:"name,omitempty"`
	Start string            `toml:"start,omitempty"`
	Port  int               `toml:"port,omitzero"`
	Env   map[string]string `toml:"env,omitempty"`
}

// LoadProject reads dir/homebase.toml; it returns nil, nil if there is none.
func LoadProject(dir string) (*Project, error) {
	path := filepath.Join(dir, ProjectFile)
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	p := &Project{}
	if _, err := toml.Decode(string(data), p); err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	return p, nil
}

const projectHeader = `# Local dev server settings for homebase: run ` + "`homebase`" + ` in this folder.
# The server gets its port in $PORT. https://github.com/semenov/homebase
`

func (p *Project) Save(dir string) error {
	var b bytes.Buffer
	b.WriteString(projectHeader + "\n")
	if err := toml.NewEncoder(&b).Encode(p); err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(dir, ProjectFile), b.Bytes(), 0o644)
}

// FindProjectDir returns the nearest directory from dir upwards that has a
// homebase.toml, or "".
func FindProjectDir(dir string) string {
	for d := dir; ; d = filepath.Dir(d) {
		if _, err := os.Stat(filepath.Join(d, ProjectFile)); err == nil {
			return d
		}
		if filepath.Dir(d) == d {
			return ""
		}
	}
}
