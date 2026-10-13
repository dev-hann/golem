// Shared runner for the Bubble Tea terminal client.

package cli

import (
	"context"
	"fmt"
	"os"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/dev-hann/golem/internal/config"
	"github.com/dev-hann/golem/internal/llm"
	"github.com/dev-hann/golem/internal/tui"
)

// pickerFetchTimeout bounds the model-list fetch before the TUI opens.
const pickerFetchTimeout = 5 * time.Second

// RunTUI runs the Bubble Tea terminal client against the server on socket;
// the shared body of `golem tui` and the golem-tui binary.
func RunTUI(socket string) error {
	model := tui.New(socket).WithModels(FetchModelsForPicker(LiveListModels))

	program := tea.NewProgram(model, tea.WithAltScreen())

	final, err := program.Run()
	if err != nil {
		return fmt.Errorf("cli: running tui: %w", err)
	}

	if finalModel, ok := final.(tui.Model); ok && finalModel.ConnErr() != "" {
		return fmt.Errorf("cli: tui: %s", finalModel.ConnErr())
	}

	return nil
}

// FetchModelsForPicker resolves the configured provider and lists its models
// for the alt+m picker. Failures are silent — the picker degrades to the
// current model.
func FetchModelsForPicker(list func(id, key string) ([]string, error)) []string {
	cfg, err := config.Load(config.DefaultPath())
	if err != nil {
		return nil
	}

	preset, ok := llm.Presets[cfg.Default]
	if !ok {
		return nil
	}

	key := cfg.Providers[cfg.Default].APIKey
	if key != "" {
		resolved, err := config.ResolveKey(key)
		if err != nil {
			return nil
		}

		key = resolved
	} else if preset.KeyEnv != "" {
		key = os.Getenv(preset.KeyEnv)
	}

	ctx, cancel := context.WithTimeout(context.Background(), pickerFetchTimeout)
	defer cancel()

	type result struct {
		models []string
		err    error
	}

	done := make(chan result, 1)

	go func() {
		models, err := list(cfg.Default, key)
		done <- result{models, err}
	}()

	select {
	case res := <-done:
		if res.err != nil {
			return nil
		}

		return NormalizeModels(res.models)
	case <-ctx.Done():
		return nil
	}
}
