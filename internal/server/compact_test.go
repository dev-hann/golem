package server_test

import (
	"context"
	"errors"
	"testing"

	"github.com/dev-hann/golem/internal/llm"
	"github.com/dev-hann/golem/internal/server"
	"github.com/dev-hann/golem/pkg/golemclient"
)

// ADR-0022: compact_session summarizes folded history through a bounded
// side call and appends a session_compacted barrier record. Refused
// while a turn runs or the session holds no conversation; a failing side
// call emits compact_failed and leaves history untouched.

// compactProvider routes the compaction side call (recognized by its
// system prompt) to canned summary events.
type compactProvider struct {
	scriptedProvider
	summaryEvents []llm.Event
	summaryErr    bool
	sawSummary    bool
	// summaryOverride replaces the canned summary stream (interrupt and
	// hang scenarios drive the side call from the test).
	summaryOverride func(ctx context.Context) (<-chan llm.Event, error)
}

func (p *compactProvider) Stream(
	ctx context.Context,
	req llm.Request,
) (<-chan llm.Event, error) {
	if req.SystemPrompt == server.CompactSystemPrompt {
		p.sawSummary = true

		if p.summaryOverride != nil {
			return p.summaryOverride(ctx)
		}

		if p.summaryErr {
			out := make(chan llm.Event, 1)
			out <- llm.Event{Type: llm.EventError, Err: errCompactBoom}
			close(out)

			return out, nil
		}

		events := p.summaryEvents
		out := make(chan llm.Event, len(events)+1)
		for _, ev := range events {
			out <- ev
		}
		close(out)

		return out, nil
	}

	return p.scriptedProvider.Stream(ctx, req)
}

// errCompactBoom is the canned side-call failure.
var errCompactBoom = errors.New("summary provider down")

// runTurn sends one user message into the tracked session and waits for
// the turn to end.
func (h *harness) runTurn(t *testing.T, text string) {
	t.Helper()

	if err := h.client.Send(golemclient.UserMessage{
		Type: golemclient.TypeUserMessage, Session: h.lastSession, Text: text,
	}); err != nil {
		t.Fatalf("send user_message: %v", err)
	}

	h.expectType(golemclient.TypeTurnEnd)
}

// createSessionTracked creates a session and tracks its id for runTurn.
func (h *harness) createSessionTracked() string {
	h.lastSession = h.createSession()

	return h.lastSession
}

func TestCompact_AppendsBarrierAndReplacesHistory(t *testing.T) {
	t.Parallel()

	provider := &compactProvider{
		scriptedProvider: scriptedProvider{rounds: [][]llm.Event{
			{{Type: llm.EventTextDelta, Delta: "first answer"}},
			{{Type: llm.EventTextDelta, Delta: "second answer"}},
		}},
		summaryEvents: []llm.Event{{Type: llm.EventTextDelta, Delta: "THE SUMMARY"}},
	}
	h := newHarness(t, provider)
	sess := h.createSessionTracked()

	h.runTurn(t, "first question")
	h.runTurn(t, "second question")

	if err := h.client.Send(golemclient.CompactSession{
		Type: golemclient.TypeCompactSession, Session: sess,
	}); err != nil {
		t.Fatalf("send compact_session: %v", err)
	}

	compacted := h.expectType(golemclient.TypeSessionCompacted).(golemclient.SessionCompacted)
	if compacted.Summary != "THE SUMMARY" {
		t.Errorf("summary = %q, want THE SUMMARY", compacted.Summary)
	}

	// The barrier record is persisted: a reloaded session folds to the
	// summary plus nothing else (the post-compact turn hasn't run yet).
	h.store.Invalidate(sess)
	reloaded, err := h.store.Get(sess)
	if err != nil {
		t.Fatalf("reload: %v", err)
	}
	if len(reloaded.Messages) != 1 || reloaded.Messages[0].Text != "[Context summary]\nTHE SUMMARY" {
		t.Fatalf("reloaded messages = %+v, want the bare summary", reloaded.Messages)
	}
	if reloaded.Title == "" {
		t.Error("title must survive the barrier")
	}

	if !provider.sawSummary {
		t.Error("summary side call never ran")
	}
}

func TestCompact_RejectedWhileTurnRunning(t *testing.T) {
	t.Parallel()

	provider := &compactProvider{}
	h := newHarness(t, provider)
	sess := h.createSession()

	// Start a turn that blocks until released.
	release := make(chan struct{})
	blocker := &scriptedProvider{streamFn: func(_ context.Context, _ int) <-chan llm.Event {
		out := make(chan llm.Event, 1)
		go func() {
			<-release
			out <- llm.Event{Type: llm.EventTextDelta, Delta: "done"}
			close(out)
		}()
		return out
	}}
	provider.streamFn = blocker.streamFn

	if err := h.client.Send(golemclient.UserMessage{
		Type: golemclient.TypeUserMessage, Session: sess, Text: "blocked",
	}); err != nil {
		t.Fatalf("send: %v", err)
	}
	h.expectType(golemclient.TypeTurnStart)

	if err := h.client.Send(golemclient.CompactSession{
		Type: golemclient.TypeCompactSession, Session: sess,
	}); err != nil {
		t.Fatalf("send compact_session: %v", err)
	}
	h.expectTypeError("invalid_request")

	close(release)
	h.expectType(golemclient.TypeTurnEnd)
}

func TestCompact_RejectedOnEmptySession(t *testing.T) {
	t.Parallel()

	h := newHarness(t, &compactProvider{})
	sess := h.createSession()

	if err := h.client.Send(golemclient.CompactSession{
		Type: golemclient.TypeCompactSession, Session: sess,
	}); err != nil {
		t.Fatalf("send compact_session: %v", err)
	}
	h.expectTypeError("invalid_request")
}

func TestCompact_FailedSideCallEmitsCompactFailed(t *testing.T) {
	t.Parallel()

	provider := &compactProvider{
		scriptedProvider: scriptedProvider{rounds: [][]llm.Event{
			{{Type: llm.EventTextDelta, Delta: "an answer"}},
		}},
		summaryErr: true,
	}
	h := newHarness(t, provider)
	sess := h.createSessionTracked()
	h.runTurn(t, "a question")

	if err := h.client.Send(golemclient.CompactSession{
		Type: golemclient.TypeCompactSession, Session: sess,
	}); err != nil {
		t.Fatalf("send compact_session: %v", err)
	}

	failed := h.expectType(golemclient.TypeCompactFailed).(golemclient.CompactFailed)
	if failed.Reason == "" {
		t.Error("compact_failed must carry a reason")
	}

	// History is untouched: no barrier record was appended.
	h.store.Invalidate(sess)
	reloaded, err := h.store.Get(sess)
	if err != nil {
		t.Fatalf("reload: %v", err)
	}
	if len(reloaded.Messages) != 2 {
		t.Fatalf("reloaded messages = %d, want the original 2 (history untouched)",
			len(reloaded.Messages))
	}
}

func TestCompact_RejectedOnUnknownSession(t *testing.T) {
	t.Parallel()

	h := newHarness(t, &compactProvider{})

	if err := h.client.Send(golemclient.CompactSession{
		Type: golemclient.TypeCompactSession, Session: "01AAAAAAAAAAAAAAAAAAAAAAAA",
	}); err != nil {
		t.Fatalf("send: %v", err)
	}
	h.expectTypeError("session_not_found")
}
