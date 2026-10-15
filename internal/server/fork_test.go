package server_test

import (
	"context"
	"testing"

	"github.com/dev-hann/golem/internal/llm"
	"github.com/dev-hann/golem/pkg/golemclient"
)

// forkProvider serves any number of plain text turns (fork tests never
// need tool calls — one delta plus usage closes a round).
func forkRounds(n int) [][]llm.Event {
	rounds := make([][]llm.Event, 0, n)
	for range n {
		rounds = append(rounds, []llm.Event{
			{Type: llm.EventTextDelta, Delta: "answer"},
			{Type: llm.EventUsage},
		})
	}
	return rounds
}

func TestServer_ForkSession_RepliesAndChildLoads(t *testing.T) {
	t.Parallel()

	provider := &scriptedProvider{rounds: forkRounds(3)}
	h := newHarness(t, provider)
	parent := h.createSessionTracked()

	h.runTurn(t, "first question")
	h.runTurn(t, "second question")

	if err := h.client.Send(golemclient.ForkSession{
		Type: golemclient.TypeForkSession, Session: parent,
	}); err != nil {
		t.Fatalf("send fork_session: %v", err)
	}

	forked := h.expectType(golemclient.TypeSessionForked).(golemclient.SessionForked)
	if forked.Parent != parent {
		t.Errorf("forked.Parent = %q, want %q", forked.Parent, parent)
	}

	if forked.Session == parent || forked.Session == "" {
		t.Fatalf("forked.Session = %q, want a fresh child id", forked.Session)
	}

	// The child loads: meta plus a history batch carrying the copied
	// prefix — events self-referential to the child id.
	if err := h.client.Send(golemclient.LoadSession{
		Type: golemclient.TypeLoadSession, Session: forked.Session,
	}); err != nil {
		t.Fatalf("send load_session: %v", err)
	}

	meta := h.expectType(golemclient.TypeSessionMeta).(golemclient.SessionMeta)
	if meta.Session != forked.Session {
		t.Errorf("meta.Session = %q, want child %q", meta.Session, forked.Session)
	}

	history := h.expectType(golemclient.TypeHistory).(golemclient.History)
	if history.Session != forked.Session {
		t.Errorf("history.Session = %q, want child %q", history.Session, forked.Session)
	}

	if len(history.Events) == 0 {
		t.Fatal("history events empty, want the copied prefix")
	}

	if meta.Turns != 2 {
		t.Errorf("child turns = %d, want 2 (both parent turns copied)", meta.Turns)
	}
}

func TestServer_ForkSession_UnknownSessionRefused(t *testing.T) {
	t.Parallel()

	h := newHarness(t, &scriptedProvider{})

	if err := h.client.Send(golemclient.ForkSession{
		Type: golemclient.TypeForkSession, Session: "01AAAAAAAAAAAAAAAAAAAAAAAA",
	}); err != nil {
		t.Fatalf("send fork_session: %v", err)
	}

	if err := h.expectTypeError("session_not_found"); err != nil {
		t.Fatalf("want session_not_found: %v", err)
	}
}

func TestServer_ForkSession_EmptyParentRefused(t *testing.T) {
	t.Parallel()

	h := newHarness(t, &scriptedProvider{})
	sess := h.createSession()

	if err := h.client.Send(golemclient.ForkSession{
		Type: golemclient.TypeForkSession, Session: sess,
	}); err != nil {
		t.Fatalf("send fork_session: %v", err)
	}

	if err := h.expectTypeError("invalid_request"); err != nil {
		t.Fatalf("want invalid_request: %v", err)
	}
}

func TestServer_ForkSession_BusyParentRefused(t *testing.T) {
	t.Parallel()

	provider := &scriptedProvider{rounds: [][]llm.Event{
		{{Type: llm.EventTextDelta, Delta: "one"}, {Type: llm.EventUsage}},
		{{Type: llm.EventTextDelta, Delta: "two"}, {Type: llm.EventUsage}},
	}}
	h := newHarness(t, provider)
	sess := h.createSessionTracked()

	// Start a turn that blocks until released.
	release := make(chan struct{})
	provider.streamFn = func(_ context.Context, _ int) <-chan llm.Event {
		out := make(chan llm.Event, 2)
		go func() {
			<-release
			out <- llm.Event{Type: llm.EventTextDelta, Delta: "answer"}
			out <- llm.Event{Type: llm.EventUsage}
			close(out)
		}()
		return out
	}

	if err := h.client.Send(golemclient.UserMessage{
		Type: golemclient.TypeUserMessage, Session: sess, Text: "question",
	}); err != nil {
		t.Fatalf("send user_message: %v", err)
	}

	h.expectType(golemclient.TypeTurnStart)

	if err := h.client.Send(golemclient.ForkSession{
		Type: golemclient.TypeForkSession, Session: sess,
	}); err != nil {
		t.Fatalf("send fork_session: %v", err)
	}

	if err := h.expectTypeError("invalid_request"); err != nil {
		t.Fatalf("want invalid_request: %v", err)
	}

	close(release)
	h.expectType(golemclient.TypeTurnEnd)
}

func TestServer_ForkChildTurnsRunIndependently(t *testing.T) {
	t.Parallel()

	provider := &scriptedProvider{rounds: forkRounds(3), titleFailures: 100}
	h := newHarness(t, provider)
	parent := h.createSessionTracked()

	h.runTurn(t, "first question")

	parentLines := func() int {
		raw, err := h.store.RawEvents(parent)
		if err != nil {
			t.Fatalf("RawEvents(parent): %v", err)
		}
		return len(raw)
	}

	before := parentLines()

	if err := h.client.Send(golemclient.ForkSession{
		Type: golemclient.TypeForkSession, Session: parent,
	}); err != nil {
		t.Fatalf("send fork_session: %v", err)
	}

	forked := h.expectType(golemclient.TypeSessionForked).(golemclient.SessionForked)

	// A turn in the child appends to the child file only.
	if err := h.client.Send(golemclient.UserMessage{
		Type: golemclient.TypeUserMessage, Session: forked.Session, Text: "new direction",
	}); err != nil {
		t.Fatalf("send user_message: %v", err)
	}

	h.expectType(golemclient.TypeTurnEnd)

	if after := parentLines(); after != before {
		t.Errorf("parent lines = %d after child turn, want %d (untouched)", after, before)
	}

	child, err := h.store.Get(forked.Session)
	if err != nil {
		t.Fatalf("Get(child): %v", err)
	}

	if child.Turns != 2 {
		t.Errorf("child turns = %d, want 2 (copied + its own)", child.Turns)
	}
}
