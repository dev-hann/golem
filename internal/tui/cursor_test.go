package tui

import (
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"
)

// The 8.2 tests assert cursor positions against the rendered screen:
// every expected cell is derived by locating typed text in the actual
// View().Content lines (acceptance criterion 3 — no hand-computed
// constants), so padScreen, the box border, and the prompt offsets are
// all covered by construction.

// boxTopRow finds the composer box's top-border row in the rendered
// screen (0-based line index). The rounded border's top-left corner
// identifies it; the search runs bottom-up because the box is the last
// layout block.
func boxTopRow(t *testing.T, screen string) int {
	t.Helper()

	rows := strings.Split(screen, "\n")

	for i := len(rows) - 1; i >= 0; i-- {
		if strings.Contains(ansi.Strip(rows[i]), "╭") {
			return i
		}
	}

	t.Fatalf("composer box not found in screen:\n%s", screen)
	return 0
}

// cellAfter returns the screen X of the cell right after needle's last
// rune, in the first box content row (below the top border) containing
// needle. cellAt returns the cell AT needle's last rune. Both count
// display width from column 0 of the row.
func cellAfter(t *testing.T, screen, needle string) (x, y int) {
	t.Helper()

	rows := strings.Split(screen, "\n")
	top := boxTopRow(t, screen)

	for i := top + 1; i < len(rows); i++ {
		stripped := ansi.Strip(rows[i])
		if idx := strings.Index(stripped, needle); idx >= 0 {
			return ansi.StringWidth(stripped[:idx+len(needle)]), i
		}
	}

	t.Fatalf("needle %q not found in box content:\n%s", needle, screen)
	return 0, 0
}

// cellAt returns the screen X of needle's first rune's cell.
func cellAt(t *testing.T, screen, needle string) (x, y int) {
	t.Helper()

	rows := strings.Split(screen, "\n")
	top := boxTopRow(t, screen)

	for i := top + 1; i < len(rows); i++ {
		stripped := ansi.Strip(rows[i])
		if idx := strings.Index(stripped, needle); idx >= 0 {
			return ansi.StringWidth(stripped[:idx]), i
		}
	}

	t.Fatalf("needle %q not found in box content:\n%s", needle, screen)
	return 0, 0
}

// promptEndCell returns the cell right after the "┃ " prompt on the
// box's first content row — the empty composer's caret position.
func promptEndCell(t *testing.T, screen string) (x, y int) {
	t.Helper()

	rows := strings.Split(screen, "\n")
	top := boxTopRow(t, screen)
	stripped := ansi.Strip(rows[top+1])

	idx := strings.Index(stripped, "┃ ")
	if idx < 0 {
		t.Fatalf("prompt not found in first content row:\n%s", screen)
	}

	return ansi.StringWidth(stripped[:idx]) + ansi.StringWidth("┃ "), top + 1
}

// TestView_CursorAtComposerCaret_Table asserts View().Cursor positions
// the terminal cursor exactly at the rendered caret: before the first
// character, mid-line after arrow keys, and on the correct soft-wrapped
// row.
func TestView_CursorAtComposerCaret_Table(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name  string
		width int
		typed string
		// moveLeft: how many times CursorLeft runs after typing (0 = caret
		// at end).
		moveLeft int
		// needle: text located in the render; the caret sits at (after) or
		// at (at) its edge.
		needle string
		at     bool
	}{
		{name: "empty input caret after prompt", width: 80, typed: "", needle: ""},
		{name: "typed line caret after last rune", width: 80, typed: "helloz", needle: "helloz"},
		{name: "caret mid-line after arrow key", width: 80, typed: "hello", moveLeft: 3, needle: "he"},
		{name: "caret on soft-wrapped second row", width: 30, typed: strings.Repeat("q", 40) + "Z", needle: "Z"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			m := New("/tmp/unused.sock")
			m.resize(tt.width, 24)

			if tt.typed != "" {
				m.input.SetValue(tt.typed)
				for range tt.moveLeft {
					m.input, _ = m.input.Update(tea.KeyPressMsg{Code: tea.KeyLeft})
				}
				m.syncInputHeight()
			}

			view := m.View()
			if view.Cursor == nil {
				t.Fatalf("View().Cursor is nil in focused chat mode")
			}

			var wantX, wantY int

			switch {
			case tt.typed == "":
				wantX, wantY = promptEndCell(t, view.Content)
			case tt.at:
				wantX, wantY = cellAt(t, view.Content, tt.needle)
			default:
				wantX, wantY = cellAfter(t, view.Content, tt.needle)
			}

			if got := view.Cursor.Position; got.X != wantX || got.Y != wantY {
				t.Errorf("cursor at (%d,%d), want (%d,%d) — screen:\n%s",
					got.X, got.Y, wantX, wantY, view.Content)
			}
		})
	}
}

// TestView_CursorHiddenInModalsAndErrors verifies the cursor is nil
// while any modal is open, while blurred, and in the conn-error state.
func TestView_CursorHiddenInModalsAndErrors(t *testing.T) {
	t.Parallel()

	states := []struct {
		name string
		prep func(*Model)
	}{
		{"sessions modal", func(m *Model) { m.mode = modeSessions }},
		{"models modal", func(m *Model) { m.mode = modeModels }},
		{"command palette", func(m *Model) { m.mode = modeCommand }},
		{"blurred", func(m *Model) { m.input.Blur() }},
		{"conn error", func(m *Model) { m.connErr = "socket refused" }},
	}

	for _, st := range states {
		t.Run(st.name, func(t *testing.T) {
			t.Parallel()

			m := New("/tmp/unused.sock")
			m.resize(80, 24)
			st.prep(&m)

			if c := m.View().Cursor; c != nil {
				t.Errorf("%s: View().Cursor = %+v, want nil", st.name, c)
			}
		})
	}
}

// TestView_CursorUnderStickyPin asserts the frame offset accounts for
// the sticky question bar: with a pinned sticky line the composer sits
// one row lower on screen, and the pinned cursor must agree with the
// rendered box even then.
func TestView_CursorUnderStickyPin(t *testing.T) {
	t.Parallel()

	m, _ := wired(t)
	m = resize(m, 80, 24)

	// Same setup as TestModel_ToolBlock_ClickAccountsForStickyHeader: a
	// question, enough answer to push it off-screen, and a scroll that
	// pins the sticky bar above the viewport.
	m.blocks = []block{
		{kind: blockUser, text: "fix the bug"},
		{kind: blockAssistant, text: strings.Repeat("answer line\n", 60)},
	}
	m.refreshView()
	// Scroll past the question — the sticky bar pins above the viewport.
	m.vp.SetYOffset(m.userSpans[0].end)
	m.refreshView()

	if m.stickyText() != "fix the bug" {
		t.Fatalf("precondition: sticky header must be visible, got %q", m.stickyText())
	}

	m.input.SetValue("typed under pin")
	m.syncInputHeight()

	view := m.View()
	if view.Cursor == nil {
		t.Fatal("View().Cursor is nil under the sticky pin")
	}

	wantX, wantY := cellAfter(t, view.Content, "typed under pin")
	if got := view.Cursor.Position; got.X != wantX || got.Y != wantY {
		t.Errorf("cursor at (%d,%d), want (%d,%d) under sticky pin — screen:\n%s",
			got.X, got.Y, wantX, wantY, view.Content)
	}
}

// the caret as the box grows around multi-row input, staying inside the
// box.
func TestView_CursorFollowsComposerGrowth(t *testing.T) {
	t.Parallel()

	m := New("/tmp/unused.sock")
	m.resize(80, 24)

	m.input.SetValue("one")
	m.syncInputHeight()
	single := m.View()
	_, singleY := cellAfter(t, single.Content, "one")

	m.input.SetValue("one\ntwo\nthreeZ")
	m.syncInputHeight()
	grown := m.View()

	grownX, grownY := cellAfter(t, grown.Content, "threeZ")
	if c := grown.Cursor; c == nil {
		t.Fatal("View().Cursor is nil with grown composer")
	} else if c.X != grownX || c.Y != grownY {
		t.Errorf("grown cursor at (%d,%d), want (%d,%d) — screen:\n%s",
			c.X, c.Y, grownX, grownY, grown.Content)
	}

	// The caret follows the box: relative to the box top, the caret moved
	// from the first content row (single-line) to the third (three lines
	// typed) — the box grew upward around it, so absolute Y alone would
	// barely move.
	singleTop, grownTop := boxTopRow(t, single.Content), boxTopRow(t, grown.Content)
	if relSingle, relGrown := singleY-singleTop, grownY-grownTop; relGrown-relSingle < 2 {
		t.Errorf("caret row inside box advanced by %d (row %d → %d), want >= 2 as the box grew",
			relGrown-relSingle, relSingle, relGrown)
	}

	if grownY <= grownTop || grownY >= grownTop+1+m.inputRows+1 {
		t.Errorf("caret row %d outside box [%d,%d):\n%s", grownY, grownTop, grownTop+1+m.inputRows+1, grown.Content)
	}
}
