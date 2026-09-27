// shipd is the server-side helper installed by ship. It is not meant to be run by hand.
package main

import (
	"os"

	"github.com/semenov/ship/internal/agent"
)

func main() {
	os.Exit(agent.Main(os.Args[1:]))
}
