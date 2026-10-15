// Package cli implements golem's command-line entrypoints. It wires real
// dependencies and renders agent events as plain terminal output.
package cli

import (
	"context"
	"fmt"
	"io"

	"github.com/dev-hann/golem/internal/agent"
	"github.com/dev-hann/golem/internal/llm"
	"github.com/dev-hann/golem/internal/tools"
)

// AskOptions configures a one-shot ask invocation.
type AskOptions struct {
	Provider llm.Provider
	Model    string
	Root     string
	Prompt   string
	Stdout   io.Writer
	Stderr   io.Writer
}

// Ask runs one agent turn and writes streamed text to Stdout and progress
// (tool activity, errors, usage) to Stderr.
func Ask(ctx context.Context, opts AskOptions) error {
	if opts.Prompt == "" {
		return fmt.Errorf("cli: empty prompt")
	}

	registry, err := tools.NewRegistry(
		tools.ReadTool{Root: opts.Root},
		tools.WriteTool{Root: opts.Root},
		tools.EditTool{Root: opts.Root},
		tools.BashTool{Root: opts.Root},
		tools.GrepTool{Root: opts.Root},
		tools.GlobTool{Root: opts.Root},
		tools.WebFetchTool{},
	)
	if err != nil {
		return fmt.Errorf("cli: building tools: %w", err)
	}

	ag := agent.New(opts.Provider, registry, opts.Model,
		agent.WithInstructions(agent.DiscoverInstructions(opts.Root)))
	for event := range ag.Run(ctx, nil, opts.Prompt) {
		if renderErr := renderEvent(opts.Stdout, opts.Stderr, event); renderErr != nil {
			return fmt.Errorf("cli: rendering: %w", renderErr)
		}
	}

	return nil
}

// renderEvent writes one agent event. Text goes to stdout, everything else to
// stderr.
func renderEvent(stdout, stderr io.Writer, event agent.Event) error {
	switch event.Type {
	case agent.EventTextDelta:
		_, err := fmt.Fprint(stdout, event.Delta)
		return err
	case agent.EventToolCallStart:
		_, err := fmt.Fprintf(stderr, "→ %s %s\n", event.Tool, compactArgs(event.Args))
		return err
	case agent.EventToolCallEnd:
		status := "✓"
		if !event.OK {
			status = "✗"
		}

		_, err := fmt.Fprintf(stderr, "%s %s\n", status, event.Tool)

		return err
	case agent.EventError:
		_, err := fmt.Fprintf(stderr, "error: %v\n", event.Err)
		return err
	case agent.EventTurnEnd:
		_, err := fmt.Fprintf(stderr, "\n[tokens: in=%d out=%d]\n",
			event.InputTokens, event.OutputTokens)

		return err
	case agent.EventTurnStart:
		return nil
	default:
		return nil
	}
}

// compactArgs shortens tool arguments for progress lines.
func compactArgs(args []byte) string {
	const maxLen = 80

	if len(args) == 0 {
		return ""
	}

	if len(args) <= maxLen {
		return string(args)
	}

	return string(args[:maxLen]) + "…"
}
