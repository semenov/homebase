// ship deploys web apps to your own server over SSH.
package main

import (
	"os"

	"github.com/vsemenov/ship/internal/cli"
)

func main() {
	os.Exit(cli.Main())
}
