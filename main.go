// homebase runs your projects: dev servers on macOS as launchd agents with
// URLs (http://<name>.localhost, http://<name>.local and, when shared,
// https://<name>.dev.<domain> through your server), and zero-downtime Docker
// deploys to your own server over SSH (https://<name>.<domain>).
package main

import (
	"os"

	"github.com/semenov/homebase/internal/cli"
)

func main() { os.Exit(cli.Main()) }
