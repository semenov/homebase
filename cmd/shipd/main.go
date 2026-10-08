// shipd is the server-side helper installed by homebase. It is not meant to be run by hand.
package main

import (
	"os"

	"github.com/semenov/homebase/internal/agent"
)

func main() {
	os.Exit(agent.Main(os.Args[1:]))
}
