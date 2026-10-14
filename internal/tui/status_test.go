package tui

import (
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/dev-hann/golem/pkg/golemclient"
)

// deliverStatus feeds one event and returns the model's status line text
// (whitespace-collapsed) for substring assertions.
func statusAfter(t *testing.T, m Model, ev any) string {
	t.Helper()
	m = deliver(t, m, ev)
	return strings.Join(strings.Fields(m.statusLine()), " ")
}

func TestStatus_ThinkingAfterTurnStart(t *testing.T) {
	t.Parallel()
	m, _ := wired(t)
	m = resize(m, 80, 24)
	m = deliver(t, m, golemclient.TurnStart{
		Type: golemclient.TypeTurnStart, Provider: "zai", Model: "glm-5.3",
	})

	if !strings.Contains(m.statusLine(), "thinking") {
		t.Errorf("status after turn_start should mention thinking, got %q", m.statusLine())
	}
}

func TestStatus_RespondingWhileStreaming(t *testing.T) {
	t.Parallel()
	m, _ := wired(t)
	m = resize(m, 80, 24)
	m = deliver(t, m, golemclient.TurnStart{Type: golemclient.TypeTurnStart})
	line := statusAfter(t, m, golemclient.TextDelta{Type: golemclient.TypeTextDelta, Delta: "hi"})

	if !strings.Contains(line, "responding") {
		t.Errorf("status during text stream should mention responding, got %q", line)
	}
}

func TestStatus_ToolShowsNameAndArg(t *testing.T) {
	t.Parallel()
	m, _ := wired(t)
	m = resize(m, 80, 24)
	m = deliver(t, m, golemclient.TurnStart{Type: golemclient.TypeTurnStart})
	m = deliver(t, m, golemclient.TextDelta{Type: golemclient.TypeTextDelta, Delta: "hi"})
	line := statusAfter(t, m, golemclient.ToolCallStart{
		Type: golemclient.TypeToolCallStart, CallID: "c1", Tool: "bash",
		Args: []byte(`{"command":"go test ./..."}`),
	})

	for _, want := range []string{"bash", "go test ./..."} {
		if !strings.Contains(line, want) {
			t.Errorf("tool status should contain %q, got %q", want, line)
		}
	}
}

func TestStatus_ToolClearsOnCallEnd(t *testing.T) {
	t.Parallel()
	m, _ := wired(t)
	m = resize(m, 80, 24)
	m = deliver(t, m, golemclient.TurnStart{Type: golemclient.TypeTurnStart})
	m = deliver(t, m, golemclient.ToolCallStart{
		Type: golemclient.TypeToolCallStart, CallID: "c1", Tool: "bash",
		Args: []byte(`{"command":"go build"}`),
	})
	m = deliver(t, m, golemclient.TextDelta{Type: golemclient.TypeTextDelta, Delta: "done"})
	line := statusAfter(t, m, golemclient.ToolCallEnd{
		Type: golemclient.TypeToolCallEnd, CallID: "c1", OK: true,
	})

	if strings.Contains(line, "bash") || strings.Contains(line, "go build") {
		t.Errorf("tool status should clear after tool_call_end, got %q", line)
	}
	if !strings.Contains(line, "responding") {
		t.Errorf("post-tool status should return to responding, got %q", line)
	}
}

func TestStatus_IdleShowsModelAndUsageAfterTurnEnd(t *testing.T) {
	t.Parallel()
	m, _ := wired(t)
	m = resize(m, 80, 24)
	m = deliver(t, m, golemclient.TurnStart{
		Type: golemclient.TypeTurnStart, Provider: "zai", Model: "glm-5.3",
	})
	m = deliver(t, m, golemclient.TextDelta{Type: golemclient.TypeTextDelta, Delta: "hi"})
	line := statusAfter(t, m, golemclient.TurnEnd{
		Type:  golemclient.TypeTurnEnd,
		Usage: golemclient.Usage{InputTokens: 1200, OutputTokens: 3400},
	})

	for _, want := range []string{"glm-5.3", "1.2k", "3.4k"} {
		if !strings.Contains(line, want) {
			t.Errorf("idle status should contain %q, got %q", want, line)
		}
	}
}

func TestStatus_ErrorEventReturnsToIdle(t *testing.T) {
	t.Parallel()
	m, _ := wired(t)
	m = resize(m, 80, 24)
	m = deliver(t, m, golemclient.TurnStart{Type: golemclient.TypeTurnStart})
	line := statusAfter(t, m, golemclient.ErrorEvent{
		Type: golemclient.TypeError, Message: "boom",
	})

	if strings.Contains(line, "thinking") || strings.Contains(line, "responding") {
		t.Errorf("error should reset the status to idle, got %q", line)
	}
}

func TestStatus_InterruptedTurnStopsTicking(t *testing.T) {
	t.Parallel()
	m, _ := wired(t)
	m = resize(m, 80, 24)
	m = deliver(t, m, golemclient.TurnStart{Type: golemclient.TypeTurnStart})
	m = deliver(t, m, golemclient.ErrorEvent{
		Type: golemclient.TypeError, Code: "interrupted", Message: "interrupted",
	})

	next, _ := m.Update(statusTickMsg{})
	if m2, ok := next.(Model); ok {
		m = m2
	}

	if strings.Contains(m.statusLine(), "thinking") {
		t.Errorf("interrupted turn should stop ticking, got %q", m.statusLine())
	}
}

func TestStatus_StaleTickIgnored(t *testing.T) {
	t.Parallel()
	m, _ := wired(t)
	m = resize(m, 80, 24)
	m = deliver(t, m, golemclient.TurnStart{Type: golemclient.TypeTurnStart})

	next, _ := m.Update(statusTickMsg{gen: 999})
	m2, _ := next.(Model)

	if !m2.turnActive {
		t.Fatal("stale tick should not end the turn")
	}
}

func TestStatus_ElapsedSecondsGrowWhileActive(t *testing.T) {
	t.Parallel()
	m, _ := wired(t)
	m = resize(m, 80, 24)
	m = deliver(t, m, golemclient.TurnStart{Type: golemclient.TypeTurnStart})

	m.turnStart = m.turnStart.Add(-5 * time.Second)

	next, _ := m.Update(statusTickMsg{gen: 0})
	m2, _ := next.(Model)

	if !strings.Contains(m2.statusLine(), "5s") {
		t.Errorf("elapsed time should render after a tick, got %q", m2.statusLine())
	}
}

func TestStatus_NoInterruptHintInStrip(t *testing.T) {
	t.Parallel()
	m, _ := wired(t)
	m = resize(m, 80, 24)
	m = deliver(t, m, golemclient.TurnStart{Type: golemclient.TypeTurnStart})

	if strings.Contains(m.statusLine(), "esc") {
		t.Errorf("the strip carries agent state only, no key hints: %q", m.statusLine())
	}
}

func TestStatus_NewSessionResetsStatus(t *testing.T) {
	t.Parallel()
	m, _ := wired(t)
	m = resize(m, 80, 24)
	m = deliver(t, m, golemclient.SessionMeta{Type: golemclient.TypeSessionMeta, Session: "s1"})
	m = deliver(t, m, golemclient.TurnStart{
		Type: golemclient.TypeTurnStart, Provider: "zai", Model: "glm-5.3",
	})

	m = arm(m) // ctrl+x n — leader sequence, not plain typing
	next, cmd := m.Update(rkey('n'))
	if m2, ok := next.(Model); ok {
		m = m2
	}

	if cmd == nil {
		t.Fatal("new session should return a send cmd")
	}

	if m.turnActive || strings.Contains(m.statusLine(), "glm-5.3") {
		t.Errorf("new session should reset status, got active=%v %q", m.turnActive, m.statusLine())
	}
}

func TestStatus_TurnActiveWithoutStartShowsThinking(t *testing.T) {
	t.Parallel()
	m, _ := wired(t)
	m = resize(m, 80, 24)
	m.turnActive = true

	if !strings.Contains(m.statusLine(), "thinking") {
		t.Errorf("active turn without explicit start should default to thinking, got %q", m.statusLine())
	}
}

func TestStatus_EscInterruptsActiveTurn(t *testing.T) {
	t.Parallel()
	m, fc := wired(t)
	m = resize(m, 80, 24)
	m = deliver(t, m, golemclient.TurnStart{Type: golemclient.TypeTurnStart})
	_ = key(t, m, tea.KeyMsg{Type: tea.KeyEscape})

	if _, ok := lastSent(t, fc).(golemclient.Interrupt); !ok {
		t.Errorf("esc should send Interrupt, got %#v", lastSent(t, fc))
	}
}

func TestStatus_SessionSwitchResetsStatus(t *testing.T) {
	t.Parallel()
	m, fc := wired(t)
	m = resize(m, 100, 40)
	m = deliver(t, m, golemclient.SessionMeta{Type: golemclient.TypeSessionMeta, Session: "s1"})

	// Session A completes a turn: idle status carries its model and usage.
	m = deliver(t, m, golemclient.TurnStart{
		Type: golemclient.TypeTurnStart, Provider: "zai", Model: "glm-5.3",
	})
	m = deliver(t, m, golemclient.TextDelta{Type: golemclient.TypeTextDelta, Delta: "hi"})
	m = deliver(t, m, golemclient.TurnEnd{
		Type:  golemclient.TypeTurnEnd,
		Usage: golemclient.Usage{InputTokens: 1200, OutputTokens: 3400},
	})

	// Switch to session B through the picker.
	m.listRequest = true
	m = deliver(t, m, golemclient.SessionList{
		Type: golemclient.TypeSessionList,
		Sessions: []golemclient.SessionSummary{
			{ID: "s1", Cwd: "/a", Turns: 1},
			{ID: "s2", Cwd: "/b", Turns: 2},
		},
	})
	m = key(t, m, tea.KeyMsg{Type: tea.KeyDown})
	m = key(t, m, tea.KeyMsg{Type: tea.KeyEnter})
	if _, ok := lastSent(t, fc).(golemclient.LoadSession); !ok {
		t.Fatal("picker enter should send a load_session")
	}

	// The status must not carry session A's identity into session B.
	line := strings.Join(strings.Fields(m.statusLine()), " ")
	for _, stale := range []string{"glm-5.3", "1.2k", "3.4k"} {
		if strings.Contains(line, stale) {
			t.Errorf("status after switch leaked session A's %q: %q", stale, line)
		}
	}
}
