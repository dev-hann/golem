// Command golem-tui is the terminal client for the golem agent server.
package main

import (
	"fmt"
	"os"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/golem-agent/golem/internal/cli"
	"github.com/golem-agent/golem/internal/tui"
)

func main() {
	socket := cli.DefaultSocketPath()
	if len(os.Args) > 1 {
		socket = os.Args[1]
	}

	program := tea.NewProgram(tui.New(socket), tea.WithAltScreen())

	final, err := program.Run()
	if err != nil {
		fmt.Fprintf(os.Stderr, "golem-tui: %v\n", err)
		os.Exit(1)
	}

	if model, ok := final.(tui.Model); ok && model.ConnErr() != "" {
		fmt.Fprintf(os.Stderr, "golem-tui: %s\n", model.ConnErr())
		os.Exit(1)
	}
}
