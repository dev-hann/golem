// Shared runner for the Bubble Tea terminal client.

package cli

import (
	"fmt"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/dev-hann/golem/internal/tui"
)

// RunTUI runs the Bubble Tea terminal client against the server on socket;
// the shared body of `golem tui` and the golem-tui binary.
func RunTUI(socket string) error {
	program := tea.NewProgram(tui.New(socket), tea.WithAltScreen())

	final, err := program.Run()
	if err != nil {
		return fmt.Errorf("cli: running tui: %w", err)
	}

	if model, ok := final.(tui.Model); ok && model.ConnErr() != "" {
		return fmt.Errorf("cli: tui: %s", model.ConnErr())
	}

	return nil
}
