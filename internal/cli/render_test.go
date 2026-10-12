package cli_test

import (
	"bytes"
	"strings"
	"testing"

	"github.com/dev-hann/golem/internal/cli"
	"github.com/dev-hann/golem/internal/llm"
)

func TestAsk_ErrorEventRenderedToStderr(t *testing.T) {
	t.Parallel()
	var stdout, stderr bytes.Buffer
	provider := &stubProvider{rounds: [][]llm.Event{
		{{Type: llm.EventError, Err: errString("provider exploded")}},
	}}
	err := cli.Ask(t.Context(), cli.AskOptions{
		Provider: provider,
		Model:    "stub",
		Root:     t.TempDir(),
		Prompt:   "hi",
		Stdout:   &stdout,
		Stderr:   &stderr,
	})
	if err != nil {
		t.Fatalf("Ask: %v", err)
	}
	if !strings.Contains(stderr.String(), "error: provider exploded") {
		t.Errorf("stderr = %q, want provider error", stderr.String())
	}
}

func TestAsk_UsageSummaryRendered(t *testing.T) {
	t.Parallel()
	var stdout, stderr bytes.Buffer
	provider := &stubProvider{rounds: [][]llm.Event{
		{{Type: llm.EventUsage, InputTokens: 5, OutputTokens: 3}},
	}}
	err := cli.Ask(t.Context(), cli.AskOptions{
		Provider: provider,
		Model:    "stub",
		Root:     t.TempDir(),
		Prompt:   "hi",
		Stdout:   &stdout,
		Stderr:   &stderr,
	})
	if err != nil {
		t.Fatalf("Ask: %v", err)
	}
	if !strings.Contains(stderr.String(), "in=5 out=3") {
		t.Errorf("stderr = %q, want usage line", stderr.String())
	}
}

// errString is a trivial error implementation.
type errString string

func (e errString) Error() string { return string(e) }
