// homebase runs local dev servers on macOS as launchd agents and gives them
// URLs: http://<name>.localhost, http://<name>.local and, when shared,
// https://<name>.<domain> through a Cloudflare Tunnel.
package main

import (
	"os"

	"github.com/semenov/homebase/internal/cli"
)

func main() { os.Exit(cli.Main()) }
