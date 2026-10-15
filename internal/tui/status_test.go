package tui

import (
	"encoding/json"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"

	"github.com/dev-hann/golem/internal/tui/kit"
	"github.com/dev-hann/golem/pkg/golemclient"
)

// deliverStatus feeds one event into m and returns the model's status
// line text (whitespace-collapsed) for substring assertions.
func statusAfter(t *testing.T, m *Model, ev any) string {
	t.Helper()
	*m = deliver(t, *m, ev)

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
	line := statusAfter(t, &m, golemclient.TextDelta{Type: golemclient.TypeTextDelta, Delta: "hi"})

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
	line := statusAfter(t, &m, golemclient.ToolCallStart{
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
	line := statusAfter(t, &m, golemclient.ToolCallEnd{
		Type: golemclient.TypeToolCallEnd, CallID: "c1", OK: true,
	})

	if strings.Contains(line, "go build") {
		t.Errorf("tool status should clear the arguments after tool_call_end, got %q", line)
	}

	// tool_call_end enters synthesizing: the finished tool's name and
	// outcome stay visible while the model digests the results.
	for _, want := range []string{"✓ bash", "working"} {
		if !strings.Contains(line, want) {
			t.Errorf("post-tool status should show synthesizing %q, got %q", want, line)
		}
	}

	// The next text delta moves past synthesizing into responding.
	line = statusAfter(t, &m, golemclient.TextDelta{Type: golemclient.TypeTextDelta, Delta: "more"})
	if !strings.Contains(line, "responding") {
		t.Errorf("text after tool results should return to responding, got %q", line)
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
	line := statusAfter(t, &m, golemclient.TurnEnd{
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
	line := statusAfter(t, &m, golemclient.ErrorEvent{
		Type: golemclient.TypeError, Message: "boom",
	})

	if strings.Contains(line, "thinking") || strings.Contains(line, "responding") {
		t.Errorf("error should reset the status to idle, got %q", line)
	}
}

func TestStatus_SynthesizingShowsFailedToolMark(t *testing.T) {
	t.Parallel()
	m, _ := wired(t)
	m = resize(m, 80, 24)
	m = deliver(t, m, golemclient.TurnStart{Type: golemclient.TypeTurnStart})
	m = deliver(t, m, golemclient.ToolCallStart{
		Type: golemclient.TypeToolCallStart, CallID: "c1", Tool: "bash",
		Args: []byte(`{"command":"go test ./..."}`),
	})
	line := statusAfter(t, &m, golemclient.ToolCallEnd{
		Type: golemclient.TypeToolCallEnd, CallID: "c1", OK: false,
	})

	if !strings.Contains(line, "✗ bash") {
		t.Errorf("failed tool should show the ✗ mark in synthesizing, got %q", line)
	}
}

func TestStatus_RetryAnnouncementRendersRetrying(t *testing.T) {
	t.Parallel()
	m, _ := wired(t)
	m = resize(m, 80, 24)
	m = deliver(t, m, golemclient.TurnStart{Type: golemclient.TypeTurnStart})
	line := statusAfter(t, &m, golemclient.Retry{
		Type: golemclient.TypeRetry, Attempt: 2, Retries: 6, DelayMs: 4000,
	})

	for _, want := range []string{"retrying 2/6", "4s"} {
		if !strings.Contains(line, want) {
			t.Errorf("retry status should contain %q, got %q", want, line)
		}
	}

	// The next text delta clears the retry state.
	line = statusAfter(t, &m, golemclient.TextDelta{Type: golemclient.TypeTextDelta, Delta: "hi"})
	if !strings.Contains(line, "responding") {
		t.Errorf("text after retry should show responding, got %q", line)
	}
}

func TestStatus_RetryOutranksToolWhileAnnounced(t *testing.T) {
	t.Parallel()
	m, _ := wired(t)
	m = resize(m, 80, 24)
	m = deliver(t, m, golemclient.TurnStart{Type: golemclient.TypeTurnStart})
	m = deliver(t, m, golemclient.ToolCallEnd{
		Type: golemclient.TypeToolCallEnd, CallID: "c1", OK: true,
	})
	line := statusAfter(t, &m, golemclient.Retry{
		Type: golemclient.TypeRetry, Attempt: 1, Retries: 6, DelayMs: 1000,
	})

	if !strings.Contains(line, "retrying") {
		t.Errorf("a retry while synthesizing should outrank it, got %q", line)
	}
}

func TestStatus_ThinkingDeltaShowsReasoning(t *testing.T) {
	t.Parallel()
	m, _ := wired(t)
	m = resize(m, 80, 24)
	m = deliver(t, m, golemclient.TurnStart{Type: golemclient.TypeTurnStart})
	line := statusAfter(t, &m, golemclient.ThinkingDelta{Type: golemclient.TypeThinkingDelta})

	if !strings.Contains(line, "reasoning") {
		t.Errorf("thinking_delta should show reasoning, got %q", line)
	}

	// Text after reasoning reports the answer underway, not more thinking.
	line = statusAfter(t, &m, golemclient.TextDelta{Type: golemclient.TypeTextDelta, Delta: "hi"})
	if !strings.Contains(line, "responding") {
		t.Errorf("text after reasoning should show responding, got %q", line)
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
	_ = key(t, m, tea.KeyPressMsg{Code: tea.KeyEscape})

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
	m = key(t, m, tea.KeyPressMsg{Code: tea.KeyDown})
	m = key(t, m, tea.KeyPressMsg{Code: tea.KeyEnter})
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

// TestStatus_ActiveStatesAnimateSpinnerFrames verifies every
// in-progress strip state animates: each tick advances the frame and
// the rendered line carries the corresponding spinner glyph instead of
// the static dot. The idle strip stays static across the same ticks.
func TestStatus_ActiveStatesAnimateSpinnerFrames(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name string
		prep func(Model) Model
		want string
	}{
		{
			name: "thinking",
			prep: func(m Model) Model {
				return deliver(t, m, golemclient.TurnStart{Type: golemclient.TypeTurnStart})
			},
			want: "thinking",
		},
		{
			name: "responding",
			prep: func(m Model) Model {
				m = deliver(t, m, golemclient.TurnStart{Type: golemclient.TypeTurnStart})
				return deliver(t, m, golemclient.TextDelta{Type: golemclient.TypeTextDelta, Delta: "hi"})
			},
			want: "responding",
		},
		{
			name: "reasoning",
			prep: func(m Model) Model {
				m = deliver(t, m, golemclient.TurnStart{Type: golemclient.TypeTurnStart})
				return deliver(t, m, golemclient.ThinkingDelta{Type: golemclient.TypeThinkingDelta})
			},
			want: "reasoning",
		},
		{
			name: "tool",
			prep: func(m Model) Model {
				m = deliver(t, m, golemclient.TurnStart{Type: golemclient.TypeTurnStart})
				return deliver(t, m, golemclient.ToolCallStart{
					Type: golemclient.TypeToolCallStart, CallID: "c1", Tool: "bash",
					Args: []byte(`{"command":"go test ./..."}`),
				})
			},
			want: "bash",
		},
		{
			name: "synthesizing",
			prep: func(m Model) Model {
				m = deliver(t, m, golemclient.TurnStart{Type: golemclient.TypeTurnStart})
				return deliver(t, m, golemclient.ToolCallEnd{
					Type: golemclient.TypeToolCallEnd, CallID: "c1", OK: true,
				})
			},
			want: "working",
		},
		{
			name: "retrying",
			prep: func(m Model) Model {
				m = deliver(t, m, golemclient.TurnStart{Type: golemclient.TypeTurnStart})
				return deliver(t, m, golemclient.Retry{
					Type: golemclient.TypeRetry, Attempt: 1, Retries: 6, DelayMs: 1000,
				})
			},
			want: "retrying",
		},
	}

	for _, tt := range cases {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			m, _ := wired(t)
			m = resize(m, 100, 40)
			m = tt.prep(m)

			// The strip shows frame 0 while the turn runs.
			line := m.statusLine()
			if !strings.Contains(line, kit.SpinnerFrames()[0]) {
				t.Errorf("%s: strip should animate, want %q in %q", tt.name, kit.SpinnerFrames()[0], line)
			}
			if !strings.Contains(line, tt.want) {
				t.Errorf("%s: strip should still name the state %q, got %q", tt.name, tt.want, line)
			}
			if strings.Contains(line, "●") {
				t.Errorf("%s: active strip must drop the static dot, got %q", tt.name, line)
			}

			// A tick advances to frame 1 and renders it.
			next, _ := m.Update(spinTickMsg{gen: m.spinGen})
			m2 := next.(Model)
			if got := m2.statusLine(); !strings.Contains(got, kit.SpinnerFrames()[1]) {
				t.Errorf("%s: after a tick the strip should show %q, got %q", tt.name, kit.SpinnerFrames()[1], got)
			}

			// Turn end freezes the strip back on the static dot.
			m3 := deliver(t, m2, golemclient.TurnEnd{Type: golemclient.TypeTurnEnd})
			if line := m3.statusLine(); !strings.Contains(line, "●") {
				t.Errorf("%s: idle strip should return to the static dot, got %q", tt.name, line)
			}
			for _, frame := range kit.SpinnerFrames() {
				if strings.Contains(m3.statusLine(), frame) {
					t.Errorf("%s: idle strip must not animate, found %q", tt.name, frame)
				}
			}
		})
	}
}

// TestStatus_IdleStripIgnoresSpinnerTicks verifies the ticker spends no
// frames while no turn runs: a tick arriving on an idle model neither
// advances the frame nor changes the strip.
func TestStatus_IdleStripIgnoresSpinnerTicks(t *testing.T) {
	t.Parallel()

	m, _ := wired(t)
	m = resize(m, 100, 40)
	m = deliver(t, m, golemclient.SessionMeta{Type: golemclient.TypeSessionMeta, Session: "s1"})
	m = deliver(t, m, golemclient.TurnStart{
		Type: golemclient.TypeTurnStart, Provider: "zai", Model: "glm-5.3",
	})
	m = deliver(t, m, golemclient.TurnEnd{Type: golemclient.TypeTurnEnd})

	before := m.statusLine()
	next, _ := m.Update(spinTickMsg{gen: m.spinGen})
	m2 := next.(Model)

	if got := m2.statusLine(); got != before {
		t.Errorf("idle strip changed on tick: %q -> %q", before, got)
	}
	if m2.spinFrame != m.spinFrame {
		t.Errorf("idle tick advanced the frame to %d, want it ignored", m2.spinFrame)
	}
}

// TestStatus_TurnStartArmsTicker verifies the first streaming event of a
// turn arms the ticker (generation bump), the tick advances the frame
// and rearms while the turn stays live, and a tick after turn end is
// dropped without rearming.
func TestStatus_TurnStartArmsTicker(t *testing.T) {
	t.Parallel()

	m, _ := wired(t)
	m = resize(m, 100, 40)

	next, cmd := m.Update(serverEventMsg{ev: golemclient.TurnStart{Type: golemclient.TypeTurnStart}})
	m2, ok := next.(Model)
	if !ok {
		t.Fatal("update must return the model")
	}
	_ = cmd // batched with listen(); executing it would block on the fake

	if m2.spinGen == m.spinGen {
		t.Error("turn start should arm the spinner ticker (generation bump)")
	}

	// The tick advances the frame and the handler rearms the next one.
	n2, rearm := m2.Update(spinTickMsg{gen: m2.spinGen})
	m3 := n2.(Model)
	if rearm == nil {
		t.Error("a live turn's tick must rearm the ticker")
	}
	if m3.spinFrame == m2.spinFrame {
		t.Errorf("tick should advance the spinner frame, still %d", m3.spinFrame)
	}

	// Turn end: the next tick is dropped, no rearm.
	m4 := deliver(t, m3, golemclient.TurnEnd{Type: golemclient.TypeTurnEnd})
	if _, cmd := m4.Update(spinTickMsg{gen: m4.spinGen}); cmd != nil {
		t.Error("a tick after turn end must not rearm the ticker")
	}
}

// Resuming a busy session must also arm the strip's animation ticker —
// the restored turn renders the animated spinner, not a frozen frame.
func TestStatus_ResumeBusySessionArmsTicker(t *testing.T) {
	t.Parallel()
	m, fc := wired(t)
	m = resize(m, 100, 40)

	m.listRequest = true
	m = deliver(t, m, golemclient.SessionList{
		Type: golemclient.TypeSessionList,
		Sessions: []golemclient.SessionSummary{
			{ID: "s1", Cwd: "/a", Turns: 1, Busy: true},
		},
	})
	m = key(t, m, tea.KeyPressMsg{Code: tea.KeyEnter}) // load s1
	if _, ok := lastSent(t, fc).(golemclient.LoadSession); !ok {
		t.Fatal("picker enter should send a load_session")
	}

	gen := m.spinGen
	m = deliver(t, m, golemclient.SessionMeta{
		Type: golemclient.TypeSessionMeta, Session: "s1", Busy: true,
	})
	m = deliver(t, m, golemclient.History{
		Type: golemclient.TypeHistory, Session: "s1",
		Events: rawLines(
			`{"type":"user_message","session":"s1","text":"hi"}`,
			`{"type":"turn_start","session":"s1","turn":"t1","provider":"zai","model":"glm-5.3"}`,
			`{"type":"text_delta","session":"s1","turn":"t1","delta":"working"}`,
		),
	})

	if !m.turnActive {
		t.Fatal("precondition: resumed session should be turnActive")
	}
	if m.spinGen == gen {
		t.Fatal("resuming a busy session should arm the animation ticker (generation bump)")
	}

	next, cmd := m.Update(spinTickMsg{gen: m.spinGen})
	m2 := next.(Model)
	if cmd == nil {
		t.Error("a resumed live turn's tick must rearm the ticker")
	}
	if m2.spinFrame == m.spinFrame {
		t.Errorf("tick should advance the spinner frame, still %d", m2.spinFrame)
	}
}

// rawLines wraps JSONL lines as a history batch's raw events.
func rawLines(lines ...string) []json.RawMessage {
	out := make([]json.RawMessage, 0, len(lines))
	for _, l := range lines {
		out = append(out, json.RawMessage(l))
	}
	return out
}

// Resuming a session whose turn is still in flight (meta busy=true, a
// replay that ends with turn_start but no turn_end) must restore the
// active-turn status — the bug: switching away and back rendered "idle"
// while the turn kept streaming.
func TestStatus_ResumeBusySessionRestoresActiveTurn(t *testing.T) {
	t.Parallel()
	m, fc := wired(t)
	m = resize(m, 100, 40)

	// Session A's turn runs; the client switches away mid-flight and back.
	m = deliver(t, m, golemclient.SessionMeta{
		Type: golemclient.TypeSessionMeta, Session: "s1",
	})
	m = deliver(t, m, golemclient.TurnStart{
		Type: golemclient.TypeTurnStart, Session: "s1", Provider: "zai", Model: "glm-5.3",
	})
	m = deliver(t, m, golemclient.TextDelta{
		Type: golemclient.TypeTextDelta, Session: "s1", Delta: "working",
	})

	// Picker switch to s2 and back to s1 (busy at load time).
	m.listRequest = true
	m = deliver(t, m, golemclient.SessionList{
		Type: golemclient.TypeSessionList,
		Sessions: []golemclient.SessionSummary{
			{ID: "s1", Cwd: "/a", Turns: 1, Busy: true},
			{ID: "s2", Cwd: "/b", Turns: 2},
		},
	})
	m = key(t, m, tea.KeyPressMsg{Code: tea.KeyEnter}) // load s1
	if _, ok := lastSent(t, fc).(golemclient.LoadSession); !ok {
		t.Fatal("picker enter should send a load_session")
	}

	m = deliver(t, m, golemclient.SessionMeta{
		Type: golemclient.TypeSessionMeta, Session: "s1", Busy: true,
	})
	m = deliver(t, m, golemclient.History{
		Type: golemclient.TypeHistory, Session: "s1",
		Events: rawLines(
			`{"type":"user_message","session":"s1","text":"hi"}`,
			`{"type":"turn_start","session":"s1","turn":"t1","provider":"zai","model":"glm-5.3"}`,
			`{"type":"text_delta","session":"s1","turn":"t1","delta":"working"}`,
		),
	})

	if !m.turnActive {
		t.Fatal("resuming a busy session should restore turnActive from the replay")
	}
	if m.modelName != "glm-5.3" {
		t.Errorf("model = %q, want glm-5.3 restored from replay", m.modelName)
	}

	line := strings.Join(strings.Fields(m.statusLine()), " ")
	if !strings.Contains(line, "responding") {
		t.Errorf("status after resume should show responding, got %q", line)
	}

	// The live turn continues on the same session: its turn_end lands and
	// the status returns to idle with the turn's usage.
	m = deliver(t, m, golemclient.TextDelta{
		Type: golemclient.TypeTextDelta, Session: "s1", Delta: " done",
	})
	m = deliver(t, m, golemclient.TurnEnd{
		Type: golemclient.TypeTurnEnd, Session: "s1",
		Usage: golemclient.Usage{InputTokens: 100, OutputTokens: 200},
	})

	if m.turnActive {
		t.Error("turn_end after resume should return the status to idle")
	}
	if !strings.Contains(m.statusLine(), "200") {
		t.Errorf("idle status should carry the resumed turn's usage, got %q", m.statusLine())
	}
}

// Resuming a session whose turn is still in flight (meta busy=true, a
// replay that ends mid-turn) must restore the active-turn status — and
// the detailed state: a trailing tool_call_end resumes as synthesizing,
// a trailing thinking_delta as reasoning.
func TestStatus_ResumeBusySessionRestoresDetailedState(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name  string
		lines []string
		want  string
	}{
		{
			name: "synthesizing after tool_call_end",
			lines: []string{
				`{"type":"user_message","session":"s1","text":"hi"}`,
				`{"type":"turn_start","session":"s1","turn":"t1","provider":"zai","model":"glm-5.3"}`,
				`{"type":"tool_call_start","session":"s1","turn":"t1","call_id":"c1","tool":"bash","args":"{\"command\":\"go build\"}"}`,
				`{"type":"tool_call_end","session":"s1","turn":"t1","call_id":"c1","ok":true}`,
			},
			want: "✓ bash",
		},
		{
			name: "reasoning after thinking_delta",
			lines: []string{
				`{"type":"user_message","session":"s1","text":"hi"}`,
				`{"type":"turn_start","session":"s1","turn":"t1","provider":"zai","model":"glm-5.3"}`,
				`{"type":"thinking_delta","session":"s1","turn":"t1"}`,
			},
			want: "reasoning",
		},
		{
			// ADR-0037: a busy session resumed while the pre-turn
			// compaction's summary side call runs (the announcement is
			// the replay's last line) restores the compacting state.
			name: "compacting after the announcement",
			lines: []string{
				`{"type":"user_message","session":"s1","text":"hi"}`,
				`{"type":"text_delta","session":"s1","turn":"t0","delta":"answer"}`,
				`{"type":"turn_end","session":"s1","turn":"t0","usage":{"input_tokens":1,"output_tokens":1}}`,
				`{"type":"compacting","session":"s1"}`,
			},
			want: "compacting",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			m, _ := wired(t)
			m = resize(m, 100, 40)

			m = deliver(t, m, golemclient.SessionMeta{
				Type: golemclient.TypeSessionMeta, Session: "s1", Busy: true,
			})
			m = deliver(t, m, golemclient.History{
				Type: golemclient.TypeHistory, Session: "s1", Events: rawLines(tt.lines...),
			})

			if !m.turnActive {
				t.Fatal("busy resume should restore turnActive")
			}

			line := strings.Join(strings.Fields(m.statusLine()), " ")
			if !strings.Contains(line, tt.want) {
				t.Errorf("resumed status should contain %q, got %q", tt.want, line)
			}
		})
	}
}

// A closed replay (turn_end present, or no in-flight turn_start) must not
// leave turnActive stuck on — sessions interrupted by a crash or loaded
// idle render idle. Only a busy meta plus an unterminated replay marks
// the turn live.
func TestStatus_ResumeIdleSessionStaysIdle(t *testing.T) {
	t.Parallel()
	m, _ := wired(t)
	m = resize(m, 100, 40)

	m = deliver(t, m, golemclient.SessionMeta{
		Type: golemclient.TypeSessionMeta, Session: "s1", Busy: false,
	})
	m = deliver(t, m, golemclient.History{
		Type: golemclient.TypeHistory, Session: "s1",
		Events: rawLines(
			`{"type":"user_message","session":"s1","text":"hi"}`,
			`{"type":"turn_start","session":"s1","turn":"t1","provider":"zai","model":"glm-5.3"}`,
			`{"type":"text_delta","session":"s1","turn":"t1","delta":"done"}`,
			`{"type":"turn_end","session":"s1","turn":"t1","usage":{"input_tokens":100,"output_tokens":200}}`,
		),
	})

	if m.turnActive {
		t.Error("idle load with a terminated replay should stay idle")
	}
	if !strings.Contains(m.statusLine(), "glm-5.3") {
		t.Errorf("idle status should carry the replay's last model, got %q", m.statusLine())
	}
	if !strings.Contains(m.statusLine(), "200") {
		t.Errorf("idle status should carry the replay's last usage, got %q", m.statusLine())
	}
}

// ADR-0037: the compacting announcement renders a live state while the
// summary side call runs — manual /compact (idle → compacting → back to
// idle on the barrier) and the automatic pre-turn path (compacting while
// awaiting turn_start) both spend up to 60 s with no other events; the
// strip must animate that window instead of reading as a stall.
func TestStatus_CompactingRendersLiveState(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name  string
		armed bool // turn already active (automatic pre-turn path)
	}{
		{name: "manual compact from idle"},
		{name: "automatic pre-turn", armed: true},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			m, _ := wired(t)
			m = resize(m, 100, 40)

			if tc.armed {
				m = deliver(t, m, golemclient.TurnStart{
					Type: golemclient.TypeTurnStart, Provider: "zai", Model: "glm-5.3",
				})
			}

			line := statusAfter(t, &m, golemclient.Compacting{
				Type: golemclient.TypeCompacting, Session: "s1",
			})

			for _, want := range []string{"compacting", "context"} {
				if !strings.Contains(line, want) {
					t.Errorf("%s: strip should mention %q, got %q", tc.name, want, line)
				}
			}

			if strings.Contains(line, "●") {
				t.Errorf("%s: live strip must drop the static dot, got %q", tc.name, line)
			}

			// The strip animates while the side call runs.
			next, _ := m.Update(spinTickMsg{gen: m.spinGen})
			m2 := next.(Model)
			if got := strings.Join(strings.Fields(m2.statusLine()), " "); !strings.Contains(got, kit.SpinnerFrames()[1]) {
				t.Errorf("%s: tick should advance the spinner, got %q", tc.name, got)
			}

			// The barrier resolves the state: back to idle (manual) or
			// on to thinking (automatic).
			var resolved string

			if tc.armed {
				resolved = statusAfter(t, &m2, golemclient.TurnStart{
					Type: golemclient.TypeTurnStart,
				})
			} else {
				resolved = statusAfter(t, &m2, golemclient.SessionCompacted{
					Type: golemclient.TypeSessionCompacted, Session: "s1", Summary: "S",
				})
			}

			if strings.Contains(resolved, "compacting") {
				t.Errorf("%s: resolved strip must leave compacting, got %q", tc.name, resolved)
			}
		})
	}
}

// The compacting state accepts an interrupt — the summary side call is
// cancellable (ADR-0025) and the user needs a visible way to stop it.
func TestStatus_CompactingEscInterrupts(t *testing.T) {
	t.Parallel()

	m, fc := wired(t)
	m = resize(m, 100, 40)

	m = deliver(t, m, golemclient.Compacting{
		Type: golemclient.TypeCompacting, Session: "s1",
	})
	_ = key(t, m, tea.KeyPressMsg{Code: tea.KeyEscape})

	if _, ok := lastSent(t, fc).(golemclient.Interrupt); !ok {
		t.Errorf("esc during compacting should send Interrupt, got %#v", lastSent(t, fc))
	}
}

// compact_failed must clear the compacting state, not strand the strip
// announcing a compaction that never lands (ADR-0037).
func TestStatus_CompactFailedClearsCompacting(t *testing.T) {
	t.Parallel()

	m, _ := wired(t)
	m = resize(m, 100, 40)

	m = deliver(t, m, golemclient.Compacting{
		Type: golemclient.TypeCompacting, Session: "s1",
	})
	line := statusAfter(t, &m, golemclient.CompactFailed{
		Type: golemclient.TypeCompactFailed, Session: "s1", Reason: "down",
	})

	if strings.Contains(line, "compacting") {
		t.Errorf("compact_failed must clear the compacting state, got %q", line)
	}
	if !strings.Contains(line, "●") {
		t.Errorf("idle strip expected after a failed compaction, got %q", line)
	}
}
