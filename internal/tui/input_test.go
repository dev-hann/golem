package tui

import (
	"testing"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/dev-hann/golem/pkg/golemclient"
)

func TestInput_MultilineAltEnter(t *testing.T) {
	t.Parallel()

	m := New("/tmp/unused.sock")

	// Type a line, then alt+enter twice must insert newlines, not submit.
	m.input.SetValue("first")
	m = key(t, m, tea.KeyMsg{Type: tea.KeyEnter, Alt: true})
	m = key(t, m, tea.KeyMsg{Type: tea.KeyEnter, Alt: true})

	if got := m.input.Value(); got != "first\n\n" {
		t.Fatalf("value = %q, want two trailing newlines", got)
	}

	if m.inputRows != 3 {
		t.Errorf("inputRows = %d, want 3 for three content lines", m.inputRows)
	}
}

func TestInput_PlainEnterStillSubmits(t *testing.T) {
	t.Parallel()

	m, _ := wired(t)
	m.input.SetValue("hello")
	m = key(t, m, tea.KeyMsg{Type: tea.KeyEnter})

	if m.input.Value() != "" {
		t.Errorf("input should reset after submit, got %q", m.input.Value())
	}
}

func TestInput_CtrlJNewline(t *testing.T) {
	t.Parallel()

	m, fc := wired(t)
	m.input.SetValue("first")
	m = key(t, m, tea.KeyMsg{Type: tea.KeyCtrlJ})

	if got := m.input.Value(); got != "first\n" {
		t.Fatalf("value = %q, want newline inserted", got)
	}

	if m.inputRows != 2 {
		t.Errorf("inputRows = %d, want 2 for two content lines", m.inputRows)
	}

	if len(fc.sent) != 0 {
		t.Errorf("ctrl+j must not submit: %+v", fc.sent)
	}
}

func TestInput_HeightGrowsWithContentAndResets(t *testing.T) {
	t.Parallel()

	m := New("/tmp/unused.sock")
	m.resize(80, 30)

	if m.inputRows != minInputHeight {
		t.Fatalf("empty input rows = %d, want %d", m.inputRows, minInputHeight)
	}

	m.input.InsertString("one\ntwo\nthree\nfour")
	m.syncInputHeight()

	if m.inputRows <= minInputHeight {
		t.Errorf("rows should grow with newlines, got %d", m.inputRows)
	}

	if m.inputRows > 30*inputMaxShareNumerator/inputMaxShareDenominator {
		t.Errorf("rows = %d exceed the terminal share cap", m.inputRows)
	}

	if m.vp.Height < viewportMinHeight {
		t.Errorf("viewport height = %d, below floor", m.vp.Height)
	}

	m.input.Reset()
	m.syncInputHeight()

	if m.inputRows != minInputHeight {
		t.Errorf("rows after reset = %d, want %d", m.inputRows, minInputHeight)
	}
}

func TestInput_PasteMultilineKeptUntilEnter(t *testing.T) {
	t.Parallel()

	m, fc := wired(t)
	m = deliver(t, m, golemclient.SessionMeta{Type: golemclient.TypeSessionMeta, Session: "s1"})
	m.input.SetValue("line one\nline two")
	m = key(t, m, tea.KeyMsg{Type: tea.KeyEnter})

	msg, ok := lastSent(t, fc).(golemclient.UserMessage)
	if !ok {
		t.Fatalf("sent = %#v, want UserMessage", lastSent(t, fc))
	}

	if msg.Text != "line one\nline two" {
		t.Errorf("text = %q, want both pasted lines preserved", msg.Text)
	}
}
