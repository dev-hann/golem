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

func TestKey_CtrlN_StartsNewSession(t *testing.T) {
	t.Parallel()

	m, fc := wired(t)
	m = deliver(t, m, golemclient.SessionMeta{Type: golemclient.TypeSessionMeta, Session: "s1"})
	m = deliver(t, m, golemclient.TurnStart{
		Type: golemclient.TypeTurnStart, Session: "s1", Provider: "zai-coding", Model: "glm-5.3",
	})
	m = deliver(t, m, golemclient.TextDelta{
		Type: golemclient.TypeTextDelta, Session: "s1", Delta: "old session text",
	})

	m = key(t, m, tea.KeyMsg{Type: tea.KeyCtrlN})

	if created, ok := lastSent(t, fc).(golemclient.CreateSession); !ok {
		t.Fatalf("sent = %#v, want CreateSession", lastSent(t, fc))
	} else if created.Type != golemclient.TypeCreateSession {
		t.Errorf("created = %#v", created)
	}

	if len(m.blocks) != 0 {
		t.Errorf("blocks after ctrl+n = %+v, want empty", m.blocks)
	}

	if m.turnActive {
		t.Error("ctrl+n must clear the turn flag locally")
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

func TestKey_CtrlP_OpensSessionPicker(t *testing.T) {
	t.Parallel()

	m, fc := wired(t)
	m = deliver(t, m, golemclient.SessionMeta{Type: golemclient.TypeSessionMeta, Session: "s1"})

	m = key(t, m, tea.KeyMsg{Type: tea.KeyCtrlP})

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

func TestKey_CtrlL_ClearsTranscriptOnly(t *testing.T) {
	t.Parallel()

	m, _ := wired(t)
	m = deliver(t, m, golemclient.SessionMeta{Type: golemclient.TypeSessionMeta, Session: "s1"})
	m = deliver(t, m, golemclient.TurnStart{
		Type: golemclient.TypeTurnStart, Session: "s1", Provider: "zai-coding", Model: "glm-5.3",
	})

	m = key(t, m, tea.KeyMsg{Type: tea.KeyCtrlL})

	if len(m.blocks) != 0 {
		t.Errorf("blocks after ctrl+l = %+v, want empty", m.blocks)
	}

	if m.sessionID != "s1" {
		t.Errorf("ctrl+l must not touch the session, got %q", m.sessionID)
	}

	if m.provider != "zai-coding" {
		t.Errorf("ctrl+l must not touch provider status, got %q", m.provider)
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

	for _, want := range []string{"ctrl+n", "ctrl+p", "ctrl+l", "alt+enter"} {
		if !strings.Contains(m.input.Placeholder, want) {
			t.Errorf("placeholder missing %q: %s", want, m.input.Placeholder)
		}
	}
}
