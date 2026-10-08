package cli

import (
	"fmt"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"

	"github.com/moby/patternmatcher"
	"github.com/moby/patternmatcher/ignorefile"

	"github.com/semenov/homebase/internal/detect"
)

// ContextInfo describes what goes into the image, so people and agents can see
// before the build what will be shipped (and, for static sites, published).
type ContextInfo struct {
	Files    int      `json:"files"`
	Bytes    int64    `json:"bytes"`
	Excluded []string `json:"excluded,omitempty"` // top-most excluded paths that exist in the project
	Warnings []string `json:"warnings,omitempty"`
}

// ignorePatterns returns the rules docker will apply to the build context: the
// project's .dockerignore, else homebase's defaults for generated Dockerfiles. A
// user Dockerfile built locally without a .dockerignore gets everything.
func ignorePatterns(dir string, plan *detect.Plan, remote bool) ([]string, error) {
	if f, err := os.Open(filepath.Join(dir, ".dockerignore")); err == nil {
		defer f.Close()
		return ignorefile.ReadAll(f)
	}
	if plan.Generated == "" && !remote {
		return nil, nil
	}
	return strings.Split(strings.TrimSpace(plan.Ignore), "\n"), nil
}

// walkContext calls visit for every path of the build context that is not
// excluded, and excluded for the top-most excluded paths.
func walkContext(dir string, patterns []string, visit func(rel string, d fs.DirEntry) error, excluded func(rel string, isDir bool)) error {
	pm, err := patternmatcher.New(patterns)
	if err != nil {
		return err
	}
	reported := map[string]bool{} // excluded dirs already reported
	underReported := func(rel string) bool {
		for p := path.Dir(rel); p != "."; p = path.Dir(p) {
			if reported[p] {
				return true
			}
		}
		return false
	}
	return filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(dir, p)
		if rel == "." {
			return nil
		}
		rel = filepath.ToSlash(rel)
		if skip, _ := pm.MatchesOrParentMatches(rel); skip {
			if excluded != nil && !underReported(rel) {
				excluded(rel, d.IsDir())
			}
			// with exclusions ("!x") a child of an excluded dir may be re-included
			if d.IsDir() && pm.Exclusions() {
				reported[rel] = true
				return nil
			}
			if d.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		return visit(rel, d)
	})
}

// sensitive names that should almost never end up in an image
var sensitiveNames = []string{".env", ".env.*", ".git", ".claude", ".cursor", ".playwright-mcp", ".npmrc", ".pypirc", "*.pem", "*.key", "id_rsa*", "id_ed25519*", ".ssh", ".aws"}

func isSensitive(rel string) bool {
	for _, part := range strings.Split(rel, "/") {
		for _, pat := range sensitiveNames {
			if ok, _ := path.Match(pat, part); ok {
				return true
			}
		}
	}
	return false
}

func inspectContext(dir string, plan *detect.Plan, remote bool) (*ContextInfo, error) {
	patterns, err := ignorePatterns(dir, plan, remote)
	if err != nil {
		return nil, err
	}
	info := &ContextInfo{}
	sensitive := map[string]bool{}
	err = walkContext(dir, patterns, func(rel string, d fs.DirEntry) error {
		if isSensitive(rel) {
			top, _, _ := strings.Cut(rel, "/")
			sensitive[top] = true
		}
		if d.Type().IsRegular() {
			if fi, err := d.Info(); err == nil {
				info.Files++
				info.Bytes += fi.Size()
			}
		}
		return nil
	}, func(rel string, isDir bool) {
		if isDir {
			rel += "/"
		}
		info.Excluded = append(info.Excluded, rel)
	})
	if err != nil {
		return nil, err
	}
	sort.Strings(info.Excluded)
	var names []string
	for n := range sensitive {
		names = append(names, n)
	}
	sort.Strings(names)
	if len(names) > 0 {
		where := "the image"
		if plan.Stack == "static" {
			where = "the image and served publicly"
		}
		info.Warnings = append(info.Warnings, fmt.Sprintf("%s will be included in %s; add them to .dockerignore", strings.Join(names, ", "), where))
	}
	return info, nil
}

// String is the one-line human summary, e.g. "9 files, 352.0 KB (excluded: .claude/, DEPLOY.md)".
func (c *ContextInfo) String() string {
	s := fmt.Sprintf("%d files, %s", c.Files, humanBytes(c.Bytes))
	if len(c.Excluded) > 0 {
		shown := c.Excluded
		more := ""
		if len(shown) > 6 {
			more = fmt.Sprintf(", +%d more", len(shown)-6)
			shown = shown[:6]
		}
		s += " (excluded: " + strings.Join(shown, ", ") + more + ")"
	}
	return s
}
