package tui

import (
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"

	"github.com/dev-hann/golem/pkg/golemclient"
)

func TestInput_MultilineAltEnter(t *testing.T) {
	t.Parallel()

	m := New("/tmp/unused.sock")

	// Type a line, then alt+enter twice must insert newlines, not submit.
	m.input.SetValue("first")
	m = key(t, m, altEnterKey())
	m = key(t, m, altEnterKey())

	if got := m.input.Value(); got != "first\n\n" {
		t.Fatalf("value = %q, want two trailing newlines", got)
	}

	if m.inputRows != 3 {
		t.Errorf("inputRows = %d, want 3 for three content lines", m.inputRows)
	}
}

// TestInput_ShiftEnterNewline pins the v2 keyboard-enhancement payoff:
// shift+enter (indistinguishable from enter under the v1 terminal
// protocol) inserts a newline exactly like alt+enter, and — unlike a
// kitty-protocol terminal's shifted runes — never uppercases the next
// typed rune.
func TestInput_ShiftEnterNewline(t *testing.T) {
	t.Parallel()

	m := New("/tmp/unused.sock")

	m.input.SetValue("first")
	m = key(t, m, shiftEnterKey())

	if got := m.input.Value(); got != "first\n" {
		t.Fatalf("value = %q, want one trailing newline", got)
	}

	m = key(t, m, textKey('a'))

	if got := m.input.Value(); got != "first\na" {
		t.Fatalf("value = %q, want the rune after the newline, unshifted", got)
	}
}

func TestInput_PlainEnterStillSubmits(t *testing.T) {
	t.Parallel()

	m, _ := wired(t)
	m.input.SetValue("hello")
	m = key(t, m, tea.KeyPressMsg{Code: tea.KeyEnter})

	if m.input.Value() != "" {
		t.Errorf("input should reset after submit, got %q", m.input.Value())
	}
}

func TestInput_CtrlJNewline(t *testing.T) {
	t.Parallel()

	m, fc := wired(t)
	m.input.SetValue("first")
	m = key(t, m, tea.KeyPressMsg{Code: 'j', Mod: tea.ModCtrl})

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

	if m.vp.Height() < viewportMinHeight {
		t.Errorf("viewport height = %d, below floor", m.vp.Height())
	}

	m.input.Reset()
	m.syncInputHeight()

	if m.inputRows != minInputHeight {
		t.Errorf("rows after reset = %d, want %d", m.inputRows, minInputHeight)
	}
}

func TestInput_HeightGrowsWithSoftWrap(t *testing.T) {
	t.Parallel()

	// Expectations are pinned to bubbles v1.0.0 wrap behavior: lines that
	// fill the usable width wrap one row earlier than a plain width split
	// suggests (trailing-space anchors, >= boundaries). The usable width is
	// terminal width minus the composer border (2) and the "┃ " prompt (2),
	// clamped to viewportMinWidth before that.
	tests := []struct {
		name  string
		width int
		value string
		want  int
	}{
		{"short line stays one row", 40, "hello", 1},
		{"long single line wraps by columns", 40, strings.Repeat("a", 100), 4}, // usable 32
		// Corrected 2026-10-16 (Phase 8.1): was 6, pinned to the pre-8.1
		// probe — which fed the textarea's content width (18) to SetWidth
		// and double-subtracted the prompt, re-wrapping at 16. The rendered
		// composer carries 9 syllables per row (content width 18): 5 rows.
		{"hangul wraps fewer syllables per row", 24, strings.Repeat("가", 40), 5},
		{"word boundary moves whole words", 12, "aaaaaaaaaa bbbbbbbbbb", 2},
		{"explicit newline plus wrap", 12, "aaaa\n" + strings.Repeat("b", 30), 3},
		{"trailing space at the boundary anchors a row", 22, strings.Repeat("a", 17) + " ", 2},
		{"width filled exactly", 22, "word " + strings.Repeat("x", 13), 2},
		{"word run at a wider boundary", 40, "aaaa bbbb cccc dddd eeee ffff gggg", 2},
		{"empty input one row", 40, "", 1},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			m := New("/tmp/unused.sock")
			m.resize(tt.width, 30)
			m.input.SetValue(tt.value)
			m.syncInputHeight()

			if m.inputRows != tt.want {
				t.Errorf("inputRows = %d, want %d (value %q, terminal width %d)",
					m.inputRows, tt.want, tt.value, tt.width)
			}
		})
	}
}

func TestInput_HeightShrinksAsWrapDisappears(t *testing.T) {
	t.Parallel()

	m := New("/tmp/unused.sock")
	m.resize(40, 30)

	m.input.SetValue(strings.Repeat("a", 100))
	m.syncInputHeight()

	if m.inputRows != 4 {
		t.Fatalf("inputRows = %d, want 4 while wrapped", m.inputRows)
	}

	m.input.SetValue("short")
	m.syncInputHeight()

	if m.inputRows != minInputHeight {
		t.Errorf("inputRows = %d, want %d after shrink", m.inputRows, minInputHeight)
	}
}

func TestInput_WrappedCapHoldsAndRewrapOnResize(t *testing.T) {
	t.Parallel()

	m := New("/tmp/unused.sock")

	// Height 10 caps the composer at 4 rows; 400 columns at width 78 need 6.
	m.resize(80, 10)
	m.input.SetValue(strings.Repeat("a", 400))
	m.syncInputHeight()

	if want := 10 * inputMaxShareNumerator / inputMaxShareDenominator; m.inputRows != want {
		t.Errorf("inputRows = %d, want capped at %d", m.inputRows, want)
	}

	if m.vp.Height() < viewportMinHeight {
		t.Errorf("viewport height = %d, below floor", m.vp.Height())
	}

	// The same text at a narrower terminal needs more rows, cap still holds.
	m.resize(40, 10)

	if want := 10 * inputMaxShareNumerator / inputMaxShareDenominator; m.inputRows != want {
		t.Errorf("inputRows = %d after narrowing, want cap %d", m.inputRows, want)
	}

	// A taller terminal lifts the cap; width 40 (usable 32) needs 4 rows
	// for 100 columns.
	m.resize(40, 30)
	m.input.SetValue(strings.Repeat("a", 100))
	m.syncInputHeight()

	if m.inputRows != 4 {
		t.Errorf("inputRows = %d, want 4 after rewrap", m.inputRows)
	}
}

func TestInput_PasteMultilineKeptUntilEnter(t *testing.T) {
	t.Parallel()

	m, fc := wired(t)
	m = deliver(t, m, golemclient.SessionMeta{Type: golemclient.TypeSessionMeta, Session: "s1"})
	m.input.SetValue("line one\nline two")
	m = key(t, m, tea.KeyPressMsg{Code: tea.KeyEnter})

	msg, ok := lastSent(t, fc).(golemclient.UserMessage)
	if !ok {
		t.Fatalf("sent = %#v, want UserMessage", lastSent(t, fc))
	}

	if msg.Text != "line one\nline two" {
		t.Errorf("text = %q, want both pasted lines preserved", msg.Text)
	}
}
