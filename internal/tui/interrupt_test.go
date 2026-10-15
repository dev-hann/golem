package tui

import (
	"strings"
	"testing"

	"github.com/dev-hann/golem/pkg/golemclient"
)

// ADR-0036: an `interrupted` error event (esc/ctrl+c during a turn) is a
// deliberate stop, not a provider failure. Live and replayed alike, it
// renders as a dim cancellation notice — never a red "error:" line — and
// still ends the turn's busy bookkeeping. Every other error code keeps
// the error treatment.

func errorLine(code, message string) golemclient.ErrorEvent {
	return golemclient.ErrorEvent{
		Type: golemclient.TypeError, Session: "s1", Turn: "t1",
		Code: code, Message: message,
	}
}

func TestEventInterruptedRendersAsCancellationNotice(t *testing.T) {
	t.Parallel()

	m, _ := wired(t)
	m.sessionID = "s1"
	m = deliver(t, m, golemclient.TurnStart{Type: golemclient.TypeTurnStart, Session: "s1"})

	m = deliver(t, m, errorLine(
		golemclient.CodeInterrupted, "turn interrupted: context canceled",
	))

	if m.turnActive {
		t.Error("an interrupted turn must clear turnActive")
	}

	if len(m.blocks) == 0 {
		t.Fatal("interrupted event produced no block")
	}

	last := m.blocks[len(m.blocks)-1]
	if last.kind != blockDim {
		t.Fatalf("interrupted block kind = %v, want blockDim (dim notice)", last.kind)
	}
	rendered := plain(last.render(testKit(), testMarkdown(), 80))
	if !strings.Contains(rendered, "stopped") {
		t.Errorf("interrupted notice = %q, want a stopped mention", rendered)
	}
	if strings.Contains(rendered, "error") || strings.Contains(rendered, "context canceled") {
		t.Errorf("interrupted notice = %q, must not leak error text", rendered)
	}
}

func TestEventOtherErrorsStillRenderAsErrors(t *testing.T) {
	t.Parallel()

	m, _ := wired(t)
	m.sessionID = "s1"
	m = deliver(t, m, golemclient.TurnStart{Type: golemclient.TypeTurnStart, Session: "s1"})

	m = deliver(t, m, errorLine(
		"provider_error", "openai stream: EOF: transient provider failure",
	))

	if len(m.blocks) == 0 {
		t.Fatal("error event produced no block")
	}

	last := m.blocks[len(m.blocks)-1]
	if last.kind != blockError {
		t.Fatalf("provider error block kind = %v, want blockError", last.kind)
	}
	if got := plain(last.render(testKit(), testMarkdown(), 80)); !strings.Contains(got, "error:") {
		t.Errorf("provider error = %q, want the error: prefix", got)
	}
}

func TestReplayInterruptedRendersAsCancellationNotice(t *testing.T) {
	t.Parallel()

	m, _ := wired(t)
	m.sessionID = "s1"

	m = deliver(t, m, golemclient.History{
		Type: golemclient.TypeHistory, Session: "s1",
		Events: mustJSON(t,
			userLine("do something long"),
			deltaLine("partway through"),
			errorLine(golemclient.CodeInterrupted, "turn interrupted: context canceled"),
		),
	})

	if m.turnActive {
		t.Error("a replay ending in interrupted must not resume the turn as active")
	}

	var sawInterrupt bool

	for _, b := range m.blocks {
		if b.kind == blockDim && strings.Contains(b.text, "stopped") {
			sawInterrupt = true
		}
		if b.kind == blockError {
			t.Errorf("replayed interruption rendered as an error block: %+v", b)
		}
	}

	if !sawInterrupt {
		t.Errorf("blocks = %+v, want a dim interrupted notice", m.blocks)
	}
}

func TestReplayOtherErrorsStillRenderAsErrors(t *testing.T) {
	t.Parallel()

	m, _ := wired(t)
	m.sessionID = "s1"

	m = deliver(t, m, golemclient.History{
		Type: golemclient.TypeHistory, Session: "s1",
		Events: mustJSON(t,
			userLine("hello"),
			deltaLine("partial"),
			errorLine("provider_error", "upstream 503"),
		),
	})

	var sawError bool

	for _, b := range m.blocks {
		if b.kind == blockError && strings.Contains(b.text, "upstream 503") {
			sawError = true
		}
	}

	if !sawError {
		t.Errorf("blocks = %+v, want an error block for provider_error", m.blocks)
	}
}
