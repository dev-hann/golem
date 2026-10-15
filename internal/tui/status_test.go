package tui

import (
	"encoding/json"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"

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
	m = key(t, m, tea.KeyMsg{Type: tea.KeyEnter}) // load s1
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
