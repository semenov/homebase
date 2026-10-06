package main

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/semenov/homebase/internal/config"
)

// splitName separates an optional leading server name from the flags after it.
func splitName(args []string) (name string, rest []string) {
	if len(args) > 0 && !strings.HasPrefix(args[0], "-") {
		return args[0], args[1:]
	}
	return "", args
}

// resolveName returns name if given, otherwise the server registered for
// the current directory.
func resolveName(cfg *config.Config, name string) (string, error) {
	if name != "" {
		_, err := cfg.Get(name)
		return name, err
	}
	return serverForCwd(cfg)
}

// serverForCwd finds the server whose dir is the current directory or the
// nearest parent of it, so commands also work from a project's subfolders.
func serverForCwd(cfg *config.Config) (string, error) {
	cwd, err := os.Getwd()
	if err != nil {
		return "", err
	}
	paths := []string{cwd}
	if real, err := filepath.EvalSymlinks(cwd); err == nil && real != cwd {
		paths = append(paths, real)
	}
	var best []string
	bestLen := -1
	for _, name := range cfg.Names() {
		dir := filepath.Clean(cfg.Servers[name].Dir)
		for _, p := range paths {
			if p != dir && !strings.HasPrefix(p, dir+string(filepath.Separator)) {
				continue
			}
			switch {
			case len(dir) > bestLen:
				best, bestLen = []string{name}, len(dir)
			case len(dir) == bestLen:
				best = append(best, name)
			}
			break
		}
	}
	switch len(best) {
	case 0:
		return "", fmt.Errorf("no server for %s — pass a name (see `homebase ls`) or register one here: homebase add -- <command>", tildify(cwd))
	case 1:
		return best[0], nil
	}
	return "", fmt.Errorf("%s has several servers (%s) — pass one of the names", tildify(cwd), strings.Join(best, ", "))
}

var nonNameRe = regexp.MustCompile(`[^a-z0-9]+`)

// nameFromDir turns a project directory into a server name: "My_App" → "my-app".
func nameFromDir(dir string) (string, error) {
	name := strings.Trim(nonNameRe.ReplaceAllString(strings.ToLower(filepath.Base(dir)), "-"), "-")
	if len(name) > 63 {
		name = strings.TrimRight(name[:63], "-")
	}
	if name == "" {
		return "", errors.New("can't derive a server name from the directory — pass one: homebase add <name> -- <command>")
	}
	return name, nil
}
