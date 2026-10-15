package tui

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/dev-hann/golem/pkg/golemclient"
)

// ADR-0022: /compact in the composer triggers a compact_session event;
// the barrier record renders as a dim summary block, both live and in
// history replay; a failure renders as an error block.

func TestSubmitCompactSendsEvent(t *testing.T) {
	t.Parallel()

	m, fx := newCompactFixture(t)
	m.sessionID = "s1"
	m.client = fx.client

	model, cmd := m.submit("/compact")
	if cmd == nil {
		t.Fatal("submit(/compact) must return a send cmd")
	}

	_ = cmd() //nolint:errcheck // execute the send now

	sent := fx.sent()
	if len(sent) != 1 {
		t.Fatalf("sent = %v, want exactly the compact_session event", sent)
	}
	comp, ok := sent[0].(golemclient.CompactSession)
	if !ok {
		t.Fatalf("sent[0] = %T, want CompactSession", sent[0])
	}
	if comp.Session != "s1" {
		t.Errorf("session = %q, want s1", comp.Session)
	}

	// The slash command itself must not become a transcript block.
	m2 := model.(Model)
	for _, b := range m2.blocks {
		if b.text == "/compact" {
			t.Error("/compact leaked into the transcript")
		}
	}
}

func TestSubmitCompactWithoutSessionIsLocalError(t *testing.T) {
	t.Parallel()

	m, _ := newCompactFixture(t)
	m.sessionID = ""
	m.client = nil

	_, cmd := m.submit("/compact")
	if cmd != nil {
		t.Error("submit(/compact) with no session must not send")
	}
}

func TestApplyEventSessionCompacted(t *testing.T) {
	t.Parallel()

	m, _ := newCompactFixture(t)
	m.sessionID = "s1"

	m.applyEvent(golemclient.SessionCompacted{
		Type: golemclient.TypeSessionCompacted, Session: "s1", Summary: "THE SUMMARY",
	})

	found := false

	for _, b := range m.blocks {
		if b.kind == blockDim && strings.Contains(b.text, "THE SUMMARY") {
			found = true
		}
	}
	if !found {
		t.Fatalf("blocks = %+v, want a dim block carrying the summary", m.blocks)
	}
}

func TestApplyEventCompactFailed(t *testing.T) {
	t.Parallel()

	m, _ := newCompactFixture(t)
	m.sessionID = "s1"

	m.applyEvent(golemclient.CompactFailed{
		Type: golemclient.TypeCompactFailed, Session: "s1", Reason: "provider down",
	})

	found := false

	for _, b := range m.blocks {
		if b.kind == blockError && strings.Contains(b.text, "provider down") {
			found = true
		}
	}
	if !found {
		t.Fatalf("blocks = %+v, want an error block carrying the reason", m.blocks)
	}
}

func TestReplayHistoryRendersCompactionBarrier(t *testing.T) {
	t.Parallel()

	m, _ := newCompactFixture(t)
	m.sessionID = "s1"

	m.replayHistory(golemclient.History{
		Type:    golemclient.TypeHistory,
		Session: "s1",
		Events: mustJSON(t,
			userLine("old question"),
			deltaLine("old answer"),
			compactedLine("REPLAYED SUMMARY"),
			userLine("new question"),
			deltaLine("new answer"),
		),
	})

	var (
		sawSummary bool
		sawOld     bool
		sawNew     bool
	)

	for _, b := range m.blocks {
		if b.kind == blockDim && strings.Contains(b.text, "REPLAYED SUMMARY") {
			sawSummary = true
		}
		if b.text == "old question" {
			sawOld = true
		}
		if b.text == "new question" {
			sawNew = true
		}
	}

	if !sawSummary {
		t.Errorf("replay missing the summary block: %+v", m.blocks)
	}
	// The view is the audit surface, not the model's context: the turns
	// before the barrier stay scrollable (the 2026-10-15 scrollback fix);
	// only the server's context fold drops them.
	if !sawOld {
		t.Error("replay must keep pre-barrier blocks in the view")
	}
	if !sawNew {
		t.Error("replay must render post-barrier blocks")
	}
}

// compactFixture wires a model with a fake client capturing sends.
type compactFixture struct {
	client *fakeClient
}

func newCompactFixture(t *testing.T) (Model, *compactFixture) {
	t.Helper()

	m := New("/tmp/unused.sock")
	fx := &compactFixture{client: newFakeClient()}

	return m, fx
}

func (f *compactFixture) sent() []any {
	return f.client.sent
}

// mustJSON marshals events into raw history lines.
func mustJSON(t *testing.T, events ...any) []json.RawMessage {
	t.Helper()

	out := make([]json.RawMessage, 0, len(events))
	for _, ev := range events {
		data, err := json.Marshal(ev)
		if err != nil {
			t.Fatalf("marshal: %v", err)
		}

		out = append(out, data)
	}

	return out
}

func userLine(text string) golemclient.UserMessage {
	return golemclient.UserMessage{Type: golemclient.TypeUserMessage, Session: "s1", Text: text}
}

func deltaLine(delta string) golemclient.TextDelta {
	return golemclient.TextDelta{Type: golemclient.TypeTextDelta, Session: "s1", Turn: "t", Delta: delta}
}

func compactedLine(summary string) golemclient.SessionCompacted {
	return golemclient.SessionCompacted{
		Type: golemclient.TypeSessionCompacted, Session: "s1", Summary: summary,
	}
}
