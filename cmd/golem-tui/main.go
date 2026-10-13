// Command golem-tui is the terminal client for the golem agent server.
// Unlike bare `golem` it never starts a server: it connects to the socket
// given as the only argument (default: the standard golem socket).
package main

import (
	"fmt"
	"os"

	"github.com/dev-hann/golem/internal/cli"
)

func main() {
	socket := cli.DefaultSocketPath()
	if len(os.Args) > 1 {
		socket = os.Args[1]
	}

	if err := cli.RunTUI(socket); err != nil {
		fmt.Fprintf(os.Stderr, "golem-tui: %v\n", err)
		os.Exit(1)
	}
}
