// Package agentbin embeds the linux shipd binaries that ship installs on servers.
// They are produced by `make agent`.
package agentbin

import (
	"embed"
	"fmt"
)

//go:embed bin
var files embed.FS

// Get returns the shipd binary for a linux GOARCH (amd64, arm64).
func Get(arch string) ([]byte, error) {
	b, err := files.ReadFile("bin/shipd-linux-" + arch)
	if err != nil {
		return nil, fmt.Errorf("this ship build does not include shipd for linux/%s (build ship with `make`)", arch)
	}
	return b, nil
}
