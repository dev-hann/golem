package cli_test

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/dev-hann/golem/internal/cli"
	"github.com/dev-hann/golem/internal/llm"
)

// stubProvider is a minimal scripted provider for CLI tests.
type stubProvider struct {
	rounds [][]llm.Event
	next   int
}

func (p *stubProvider) Stream(
	_ context.Context,
	_ llm.Request,
) (<-chan llm.Event, error) {
	if p.next >= len(p.rounds) {
		return nil, fmt.Errorf("stub: no more rounds")
	}
	round := p.rounds[p.next]
	p.next++
	out := make(chan llm.Event, len(round))
	for _, ev := range round {
		out <- ev
	}
	close(out)
	return out, nil
}

func TestAsk_StreamsTextToStdout(t *testing.T) {
	t.Parallel()
	var stdout, stderr bytes.Buffer
	provider := &stubProvider{rounds: [][]llm.Event{
		{{Type: llm.EventTextDelta, Delta: "the module is "}, {Type: llm.EventTextDelta, Delta: "golem"}},
	}}
	err := cli.Ask(t.Context(), cli.AskOptions{
		Provider: provider,
		Model:    "stub",
		Root:     t.TempDir(),
		Prompt:   "what module",
		Stdout:   &stdout,
		Stderr:   &stderr,
	})
	if err != nil {
		t.Fatalf("Ask: %v", err)
	}
	if got := stdout.String(); got != "the module is golem" {
		t.Errorf("stdout = %q", got)
	}
}

func TestAsk_ToolProgressGoesToStderr(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "f.txt"), []byte("hello\n"), 0o644); err != nil {
		t.Fatalf("setup: %v", err)
	}
	var stdout, stderr bytes.Buffer
	provider := &stubProvider{rounds: [][]llm.Event{
		{{
			Type: llm.EventToolCalls,
			ToolCalls: []llm.ToolCall{
				{ID: "1", Name: "read", Args: []byte(`{"path":"f.txt"}`)},
			},
		}},
		{{Type: llm.EventTextDelta, Delta: "done"}},
	}}
	err := cli.Ask(t.Context(), cli.AskOptions{
		Provider: provider,
		Model:    "stub",
		Root:     root,
		Prompt:   "read f",
		Stdout:   &stdout,
		Stderr:   &stderr,
	})
	if err != nil {
		t.Fatalf("Ask: %v", err)
	}
	if !strings.Contains(stdout.String(), "done") {
		t.Errorf("stdout missing answer: %q", stdout.String())
	}
	progress := stderr.String()
	if !strings.Contains(progress, "→ read") || !strings.Contains(progress, "✓ read") {
		t.Errorf("stderr missing tool progress: %q", progress)
	}
}

func TestAsk_EmptyPromptErrors(t *testing.T) {
	t.Parallel()
	err := cli.Ask(t.Context(), cli.AskOptions{Provider: &stubProvider{}, Prompt: ""})
	if err == nil || !strings.Contains(err.Error(), "empty prompt") {
		t.Errorf("expected empty-prompt error, got: %v", err)
	}
}
