package tui

import (
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/dev-hann/golem/pkg/golemclient"
)

func TestApplyEvent_IgnoresStaleSessionEvents(t *testing.T) {
	t.Parallel()

	m, _ := wired(t)
	m = deliver(t, m, golemclient.SessionMeta{Type: golemclient.TypeSessionMeta, Session: "s1"})

	// A delta from another session must not touch this transcript.
	m = deliver(t, m, golemclient.TextDelta{
		Type: golemclient.TypeTextDelta, Session: "stale", Delta: "ghost text",
	})

	for _, b := range m.blocks {
		if strings.Contains(b.text, "ghost text") {
			t.Fatalf("stale delta leaked into transcript: %+v", m.blocks)
		}
	}

	// Current-session deltas still land.
	m = deliver(t, m, golemclient.TextDelta{
		Type: golemclient.TypeTextDelta, Session: "s1", Delta: "live text",
	})

	if !strings.Contains(m.View(), "live text") {
		t.Errorf("current-session delta missing from view")
	}
}

func TestKey_LeaderN_StartsNewSession(t *testing.T) {
	t.Parallel()

	m, fc := wired(t)
	m = deliver(t, m, golemclient.SessionMeta{Type: golemclient.TypeSessionMeta, Session: "s1"})
	m = deliver(t, m, golemclient.TurnStart{
		Type: golemclient.TypeTurnStart, Session: "s1", Provider: "zai-coding", Model: "glm-5.3",
	})
	m = deliver(t, m, golemclient.TextDelta{
		Type: golemclient.TypeTextDelta, Session: "s1", Delta: "old session text",
	})

	m = arm(m)
	m = key(t, m, rkey('n'))

	if created, ok := lastSent(t, fc).(golemclient.CreateSession); !ok {
		t.Fatalf("sent = %#v, want CreateSession", lastSent(t, fc))
	} else if created.Type != golemclient.TypeCreateSession {
		t.Errorf("created = %#v", created)
	}

	if len(m.blocks) != 0 {
		t.Errorf("blocks after leader n = %+v, want empty", m.blocks)
	}

	if m.turnActive {
		t.Error("leader n must clear the turn flag locally")
	}

	// The new session's meta re-arms the client; stale s1 streaming is dropped.
	m = deliver(t, m, golemclient.SessionMeta{Type: golemclient.TypeSessionMeta, Session: "s2"})
	m = deliver(t, m, golemclient.TextDelta{
		Type: golemclient.TypeTextDelta, Session: "s1", Delta: "stale tail",
	})

	if strings.Contains(m.View(), "stale tail") || strings.Contains(m.View(), "old session text") {
		t.Errorf("stale content leaked after new session:\n%s", m.View())
	}

	if m.sessionID != "s2" {
		t.Errorf("sessionID = %q, want s2", m.sessionID)
	}
}

func TestKey_LeaderP_OpensSessionPicker(t *testing.T) {
	t.Parallel()

	m, fc := wired(t)
	m = deliver(t, m, golemclient.SessionMeta{Type: golemclient.TypeSessionMeta, Session: "s1"})

	m = arm(m)
	m = key(t, m, rkey('p'))

	if _, ok := lastSent(t, fc).(golemclient.ListSessions); !ok {
		t.Fatalf("sent = %#v, want ListSessions", lastSent(t, fc))
	}

	m = deliver(t, m, golemclient.SessionList{
		Type:     golemclient.TypeSessionList,
		Sessions: []golemclient.SessionSummary{{ID: "s1", Cwd: "/w", Turns: 3}},
	})

	if m.mode != modeSessions {
		t.Errorf("mode = %v, want sessions picker", m.mode)
	}
}

func TestKey_LeaderL_ClearsTranscriptOnly(t *testing.T) {
	t.Parallel()

	m, _ := wired(t)
	m = deliver(t, m, golemclient.SessionMeta{Type: golemclient.TypeSessionMeta, Session: "s1"})
	m = deliver(t, m, golemclient.TurnStart{
		Type: golemclient.TypeTurnStart, Session: "s1", Provider: "zai-coding", Model: "glm-5.3",
	})

	m = arm(m)
	m = key(t, m, rkey('l'))

	if len(m.blocks) != 0 {
		t.Errorf("blocks after leader l = %+v, want empty", m.blocks)
	}

	if m.sessionID != "s1" {
		t.Errorf("leader l must not touch the session, got %q", m.sessionID)
	}

	if m.provider != "zai-coding" {
		t.Errorf("leader l must not touch provider status, got %q", m.provider)
	}
}

func TestKey_Esc_InterruptsActiveTurn(t *testing.T) {
	t.Parallel()

	m, fc := wired(t)
	m = deliver(t, m, golemclient.SessionMeta{Type: golemclient.TypeSessionMeta, Session: "s1"})
	m = deliver(t, m, golemclient.TurnStart{Type: golemclient.TypeTurnStart, Session: "s1"})

	_ = key(t, m, tea.KeyMsg{Type: tea.KeyEscape})

	if interrupted, ok := lastSent(t, fc).(golemclient.Interrupt); !ok || interrupted.Session != "s1" {
		t.Fatalf("sent = %#v, want Interrupt for s1", lastSent(t, fc))
	}
}

func TestKey_Esc_IdleDoesNotQuit(t *testing.T) {
	t.Parallel()

	m, _ := wired(t)
	m = deliver(t, m, golemclient.SessionMeta{Type: golemclient.TypeSessionMeta, Session: "s1"})

	_ = key(t, m, tea.KeyMsg{Type: tea.KeyEscape})

	if m.quitting {
		t.Error("esc while idle must not quit — that stays ctrl+c's job")
	}
}

func TestPlaceholderDocumentsShortcuts(t *testing.T) {
	t.Parallel()

	m := New("/tmp/unused.sock")

	// The placeholder stays minimal: the bindings reference lives behind
	// ctrl+x ? — the documented entry point.
	for _, want := range []string{"enter", "alt+enter", "ctrl+x"} {
		if !strings.Contains(m.input.Placeholder, want) {
			t.Errorf("placeholder missing %q: %s", want, m.input.Placeholder)
		}
	}
}

func TestKey_LeaderQuestionOpensHelpModal(t *testing.T) {
	t.Parallel()

	m, _ := wired(t)
	m = resize(m, 100, 40)

	m = arm(m)
	m = key(t, m, rkey('?'))

	view := m.View()
	if !strings.Contains(view, "keys") {
		t.Fatalf("help modal missing:\n%s", view)
	}

	for _, want := range []string{
		"ctrl+x n", "ctrl+x p", "ctrl+x m", "ctrl+x x", "ctrl+x l", "ctrl+x ?", "pgup",
	} {
		if !strings.Contains(view, want) {
			t.Errorf("help modal missing %q:\n%s", want, view)
		}
	}

	if !strings.Contains(view, "╭") {
		t.Errorf("help modal not framed:\n%s", view)
	}

	// Any key closes.
	m = key(t, m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'x'}})

	if m.mode != modeChat {
		t.Errorf("mode after any-key close = %v, want chat", m.mode)
	}
}

func TestKey_LeaderMOpensModelModalAndApplies(t *testing.T) {
	t.Parallel()

	m, fc := wired(t)
	m = resize(m, 100, 40)
	m = m.WithModels([]string{"glm-5.3", "glm-5.3-flash", "glm-5.3-air"})
	m = deliver(t, m, golemclient.SessionMeta{Type: golemclient.TypeSessionMeta, Session: "s1"})
	m = deliver(t, m, golemclient.TurnStart{
		Type: golemclient.TypeTurnStart, Session: "s1", Provider: "zai-coding", Model: "glm-5.3",
	})

	m = arm(m)
	m = key(t, m, rkey('m'))

	view := m.View()
	if !strings.Contains(view, "models · zai-coding") {
		t.Fatalf("model modal missing title:\n%s", view)
	}

	for _, want := range []string{"glm-5.3", "glm-5.3-flash", "glm-5.3-air"} {
		if !strings.Contains(view, want) {
			t.Errorf("model modal missing %q:\n%s", want, view)
		}
	}

	m = key(t, m, tea.KeyMsg{Type: tea.KeyDown})
	m = key(t, m, tea.KeyMsg{Type: tea.KeyEnter})

	sent, ok := lastSent(t, fc).(golemclient.SetModel)
	if !ok || sent.Model != "glm-5.3-flash" || sent.Session != "s1" {
		t.Fatalf("sent = %#v, want SetModel glm-5.3-flash for s1", sent)
	}

	if m.mode != modeChat || m.modelName != "glm-5.3-flash" {
		t.Errorf("after apply: mode=%v model=%q", m.mode, m.modelName)
	}
}

func TestKey_LeaderMWithoutModelsShowsCurrentOnly(t *testing.T) {
	t.Parallel()

	m, _ := wired(t)
	m = resize(m, 100, 40)
	m = deliver(t, m, golemclient.TurnStart{
		Type: golemclient.TypeTurnStart, Session: "s1", Provider: "zai-coding", Model: "glm-5.3",
	})

	m = arm(m)
	m = key(t, m, rkey('m'))

	if !strings.Contains(m.View(), "glm-5.3") {
		t.Fatalf("current model missing from fallback modal:\n%s", m.View())
	}

	if !strings.Contains(m.View(), "no model list") {
		t.Errorf("fallback note missing:\n%s", m.View())
	}
}

func TestKey_LeaderUnboundKeyFallsThrough(t *testing.T) {
	t.Parallel()

	m, fc := wired(t)
	m = arm(m)
	m = key(t, m, rkey('q'))

	if m.mode != modeChat {
		t.Errorf("mode = %v, want chat", m.mode)
	}

	if got := m.input.Value(); got != "q" {
		t.Errorf("input = %q, want q — a stray arm must not swallow typing", got)
	}

	if len(fc.sent) != 0 {
		t.Errorf("unbound key sent events: %+v", fc.sent)
	}
}

func TestKey_LeaderEscCancelsWithoutInterrupt(t *testing.T) {
	t.Parallel()

	m, fc := wired(t)
	m = deliver(t, m, golemclient.TurnStart{Type: golemclient.TypeTurnStart, Session: "s1"})

	m = arm(m)
	m = key(t, m, tea.KeyMsg{Type: tea.KeyEscape})

	if m.mode != modeChat {
		t.Errorf("mode = %v, want chat", m.mode)
	}

	for _, sent := range fc.sent {
		if _, ok := sent.(golemclient.Interrupt); ok {
			t.Error("esc during a leader sequence must cancel it, not interrupt the turn")
		}
	}
}

func TestKey_LeaderTimeoutDisarms(t *testing.T) {
	t.Parallel()

	m, fc := wired(t)
	m = arm(m)               // generation 1
	m = tick(m, 1)           // the window elapses
	m = key(t, m, rkey('n')) // a plain key press now, not a command

	if len(fc.sent) != 0 {
		t.Fatalf("sent = %+v, want none — an expired sequence must not fire", fc.sent)
	}

	if got := m.input.Value(); got != "n" {
		t.Errorf("input = %q, want n typed after expiry", got)
	}
}

func TestKey_LeaderStaleTickDoesNotDisarm(t *testing.T) {
	t.Parallel()

	m, _ := wired(t)
	m = arm(m) // generation 1
	m = tick(m, 1)
	m = arm(m)     // generation 2
	m = tick(m, 1) // stale tick from the first arm

	m = key(t, m, rkey('m'))

	if m.mode != modeModels {
		t.Errorf("mode = %v, want models — a stale tick disarmed a live sequence", m.mode)
	}
}
