package server_test

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/dev-hann/golem/internal/llm"
	"github.com/dev-hann/golem/pkg/golemclient"
)

// ADR-0019: AGENTS.md files discovered for a session's cwd are appended
// to the system prompt of every turn.

func TestServer_TurnAppendsProjectAGENTSInstructions(t *testing.T) {
	t.Parallel()

	provider := newRecordingProvider([][]llm.Event{
		{{Type: llm.EventTextDelta, Delta: "ok"}},
	})
	h := newHarness(t, provider)

	if err := os.WriteFile(filepath.Join(h.root, "AGENTS.md"),
		[]byte("PROJECT-RULES-MARKER\n"), 0o644); err != nil {
		t.Fatalf("write AGENTS.md: %v", err)
	}

	sess := h.createSession()
	if err := h.client.Send(golemclient.UserMessage{
		Type: golemclient.TypeUserMessage, Session: sess, Text: "hi",
	}); err != nil {
		t.Fatalf("send: %v", err)
	}
	h.expectType(golemclient.TypeTurnEnd)

	prompt := provider.lastTurnRequest().SystemPrompt
	if prompt == "" {
		t.Fatal("no turn request recorded")
	}

	for _, marker := range []string{
		"You are golem", "PROJECT-RULES-MARKER",
	} {
		if !strings.Contains(prompt, marker) {
			t.Errorf("system prompt missing %q:\n%s", marker, prompt)
		}
	}
}

func TestServer_TurnWorksWithoutAGENTSFiles(t *testing.T) {
	t.Parallel()

	provider := newRecordingProvider([][]llm.Event{
		{{Type: llm.EventTextDelta, Delta: "ok"}},
	})
	h := newHarness(t, provider)
	sess := h.createSession()

	if err := h.client.Send(golemclient.UserMessage{
		Type: golemclient.TypeUserMessage, Session: sess, Text: "hi",
	}); err != nil {
		t.Fatalf("send: %v", err)
	}
	h.expectType(golemclient.TypeTurnEnd)

	// No AGENTS.md anywhere: the prompt stays the built-in one.
	if got := provider.lastTurnRequest().SystemPrompt; strings.Contains(got, "AGENTS.md") {
		t.Errorf("system prompt must stay built-in, got:\n%s", got)
	}
}

// recordingProvider records every request it sees; turn rounds come from
// the scripted list, anything else (title side calls) streams a title.
// The channel mutex keeps appends safe under the race detector.
type recordingProvider struct {
	scriptedProvider
	gate     chan struct{}
	requests []llm.Request
}

func newRecordingProvider(rounds [][]llm.Event) *recordingProvider {
	p := &recordingProvider{gate: make(chan struct{}, 1)}
	p.rounds = rounds
	return p
}

func (p *recordingProvider) Stream(
	ctx context.Context,
	req llm.Request,
) (<-chan llm.Event, error) {
	p.gate <- struct{}{}
	p.requests = append(p.requests, req)
	<-p.gate

	return p.scriptedProvider.Stream(ctx, req)
}

// lastTurnRequest returns the latest non-title request.
func (p *recordingProvider) lastTurnRequest() llm.Request {
	p.gate <- struct{}{}
	defer func() { <-p.gate }()

	for i := len(p.requests) - 1; i >= 0; i-- {
		req := p.requests[i]
		if strings.HasPrefix(req.SystemPrompt, "You are a title generator") {
			continue
		}

		return req
	}

	return llm.Request{}
}
