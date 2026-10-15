package tui

import (
	"errors"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"

	"github.com/dev-hann/golem/pkg/golemclient"
)

// selectionTranscript builds a connected model at 80x24 with a plain
// ten-line assistant answer fully visible (no sticky bar, no scroll).
func selectionTranscript(t *testing.T) Model {
	t.Helper()

	m, _ := wired(t)
	m = resize(m, 80, 24)
	m = deliver(t, m, golemclient.TurnStart{Type: golemclient.TypeTurnStart})
	m = deliver(t, m, golemclient.TextDelta{
		Type: golemclient.TypeTextDelta,
		Delta: strings.Join([]string{
			"l0", "l1", "l2", "l3", "l4", "l5", "l6", "l7", "l8", "l9",
		}, "\n"),
	})
	m = deliver(t, m, golemclient.TurnEnd{Type: golemclient.TypeTurnEnd})

	return m
}

// press clicks the left button on screen row y.
func press(m Model, y int) Model {
	next, _ := m.Update(tea.MouseClickMsg{Button: tea.MouseLeft, X: 0, Y: y})
	return next.(Model)
}

// dragMove moves the pointer to screen row y while the button is held.
func dragMove(m Model, y int) Model {
	next, _ := m.Update(tea.MouseMotionMsg{Button: tea.MouseLeft, X: 0, Y: y})
	return next.(Model)
}

// releaseMouse ends the drag (SGR release: same button; X10: none).
func releaseMouse(m Model) Model {
	next, _ := m.Update(tea.MouseReleaseMsg{})
	return next.(Model)
}

func TestModel_DragSelectsLines(t *testing.T) {
	t.Parallel()

	m := selectionTranscript(t)

	m = press(m, stickyTopRow(m)+2)
	if !m.selecting || m.selAnchor != 2 || m.selHead != 2 {
		t.Fatalf("press at y=2: selecting=%v anchor=%d head=%d; want true 2 2",
			m.selecting, m.selAnchor, m.selHead)
	}

	m = dragMove(m, stickyTopRow(m)+5)
	if m.selHead != 5 {
		t.Errorf("drag to y=5: head=%d; want 5", m.selHead)
	}

	got := m.selectedText()
	for _, want := range []string{"l2", "l3", "l4", "l5"} {
		if !strings.Contains(got, want) {
			t.Errorf("selectedText missing %q:\n%s", want, got)
		}
	}
	for _, not := range []string{"l1", "l6"} {
		if strings.Contains(got, not) {
			t.Errorf("selectedText must not contain %q:\n%s", not, got)
		}
	}

	// The highlight renders on the selected screen rows only.
	view := strings.Split(m.View().Content, "\n")
	for y := stickyTopRow(m) + 2; y <= stickyTopRow(m)+5; y++ {
		if !strings.Contains(view[y], "\x1b[7m") {
			t.Errorf("view row %d must carry the reverse marker:\n%q", y, view[y])
		}
	}
	for _, y := range []int{stickyTopRow(m) + 1, stickyTopRow(m) + 6} {
		if strings.Contains(view[y], "\x1b[7m") {
			t.Errorf("view row %d must not be highlighted:\n%q", y, view[y])
		}
	}
}

func TestModel_DragClampsToContent(t *testing.T) {
	t.Parallel()

	m := selectionTranscript(t)

	// Dragging far past the last content line selects through l9 only.
	m = press(m, stickyTopRow(m)+2)
	m = dragMove(m, m.vp.Height()+10)
	if m.selHead != 9 {
		t.Errorf("clamped head=%d; want 9 (last content line)", m.selHead)
	}

	// Dragging back up re-selects downward-only range from the anchor.
	m = dragMove(m, stickyTopRow(m)+3)
	if m.selHead != 3 {
		t.Errorf("head after drag back=%d; want 3", m.selHead)
	}
}

func TestModel_SelectionSkipsComposerStickyAndModals(t *testing.T) {
	t.Parallel()

	m := selectionTranscript(t)

	// The composer area is below the viewport.
	m = press(m, m.vp.Height()+2)
	if m.selecting || m.selectedText() != "" {
		t.Errorf("press on the composer must not start a selection (selecting=%v)", m.selecting)
	}

	// A modal backdrop swallows the press.
	m = press(m, stickyTopRow(m)+2)
	m = releaseMouse(m)
	m.mode = modeSessions
	m = press(m, stickyTopRow(m)+3)
	if m.selecting || m.selectedText() != "" {
		t.Errorf("press behind a modal must not start a selection (selecting=%v)", m.selecting)
	}

	// The sticky bar row is not transcript content.
	sticky := selectionTranscript(t)
	sticky.blocks = []block{
		{kind: blockUser, text: "the question"},
		{kind: blockAssistant, text: strings.Repeat("answer line\n", 60)},
	}
	sticky.refreshView()
	sticky.vp.SetYOffset(30)
	sticky.refreshView()

	if sticky.stickyText() != "the question" {
		t.Fatalf("precondition: sticky bar must be pinned, got %q", sticky.stickyText())
	}

	sticky = press(sticky, stickyTopRow(sticky)-1) // the sticky bar sits one row above the transcript
	if sticky.selecting || sticky.selectedText() != "" {
		t.Error("press on the sticky bar must not start a selection")
	}

	sticky = press(sticky, stickyTopRow(sticky))
	if !sticky.selecting || sticky.selAnchor != 30 {
		t.Errorf("press below the sticky: selecting=%v anchor=%d; want true 30",
			sticky.selecting, sticky.selAnchor)
	}
}

func TestModel_CtrlC_CopiesSelectionThenLadder(t *testing.T) {
	t.Parallel()

	m := selectionTranscript(t)

	var got string
	m.copier = func(s string) error {
		got = s
		return nil
	}

	m = press(m, stickyTopRow(m)+2)
	m = dragMove(m, stickyTopRow(m)+5)
	m = releaseMouse(m)

	next, cmd := m.Update(tea.KeyPressMsg{Code: 'c', Mod: tea.ModCtrl})
	m = next.(Model)

	if cmd == nil {
		t.Fatal("ctrl+c with a selection must run the copy cmd")
	}
	_ = cmd()

	for _, want := range []string{"l2", "l5"} {
		if !strings.Contains(got, want) {
			t.Errorf("copied text missing %q:\n%s", want, got)
		}
	}
	if strings.Contains(got, "l1") || strings.Contains(got, "l6") {
		t.Errorf("copied text must stay within the selection:\n%s", got)
	}
	if m.selectedText() != "" {
		t.Error("selection must clear after copying")
	}
	if strings.Contains(m.View().Content, "\x1b[7m") {
		t.Error("highlight must clear after copying")
	}

	// The ladder resumes: an idle empty composer asks first, then quits.
	m = key(t, m, tea.KeyPressMsg{Code: 'c', Mod: tea.ModCtrl}) // arm the confirm

	if m.quitting {
		t.Fatal("the first post-copy ctrl+c must ask, not quit")
	}

	_, quitCmd := m.Update(tea.KeyPressMsg{Code: 'c', Mod: tea.ModCtrl})
	if quitCmd == nil {
		t.Error("ctrl+c after copy should fall through to the exit ladder")
	}
}

func TestModel_CtrlC_CopyFailureShowsError(t *testing.T) {
	t.Parallel()

	m := selectionTranscript(t)

	m.copier = func(string) error { return errors.New("no clipboard") }

	m = press(m, stickyTopRow(m)+2)
	m = dragMove(m, stickyTopRow(m)+5)
	m = releaseMouse(m)

	next, cmd := m.Update(tea.KeyPressMsg{Code: 'c', Mod: tea.ModCtrl})
	m = next.(Model)
	if cmd == nil {
		t.Fatal("copy cmd missing")
	}

	msg := cmd()

	next, _ = m.Update(msg)
	m = next.(Model)

	if !strings.Contains(m.View().Content, "no clipboard") {
		t.Errorf("copy failure should surface in the transcript:\n%s", m.View().Content)
	}
}

func TestModel_ZeroWidthPressKeepsLadder(t *testing.T) {
	t.Parallel()

	m, fc := wired(t)
	m = resize(m, 80, 24)
	m = deliver(t, m, golemclient.TurnStart{Type: golemclient.TypeTurnStart})
	m = deliver(t, m, golemclient.TextDelta{Type: golemclient.TypeTextDelta, Delta: "l0\nl1\nl2"})
	m = deliver(t, m, golemclient.TurnEnd{Type: golemclient.TypeTurnEnd})

	m = press(m, stickyTopRow(m)+1)
	m = releaseMouse(m)

	if m.selectedText() != "" {
		t.Fatal("a plain click must not retain a selection")
	}

	// ctrl+c during the turn interrupts rather than copying nothing.
	m = deliver(t, m, golemclient.TurnStart{Type: golemclient.TypeTurnStart})
	m = press(m, stickyTopRow(m)+1)
	m = releaseMouse(m)

	key(t, m, tea.KeyPressMsg{Code: 'c', Mod: tea.ModCtrl})

	if _, ok := lastSent(t, fc).(golemclient.Interrupt); !ok {
		t.Fatalf("ctrl+c with zero-width click should interrupt, sent=%v", fc.sent)
	}
}

func TestModel_EscClearsSelection(t *testing.T) {
	t.Parallel()

	m := selectionTranscript(t)

	m = press(m, stickyTopRow(m)+2)
	m = dragMove(m, stickyTopRow(m)+5)

	next, _ := m.Update(tea.KeyPressMsg{Code: tea.KeyEscape})
	m = next.(Model)

	if m.selectedText() != "" {
		t.Error("esc must clear the selection")
	}
	if strings.Contains(m.View().Content, "\x1b[7m") {
		t.Error("esc must clear the highlight")
	}
}

func TestModel_ResizeClearsSelection(t *testing.T) {
	t.Parallel()

	m := selectionTranscript(t)

	m = press(m, stickyTopRow(m)+2)
	m = dragMove(m, stickyTopRow(m)+5)
	m = resize(m, 100, 30)

	if m.selectedText() != "" {
		t.Error("resize must clear the selection (line indices rewrap)")
	}
}

func TestModel_ToolRowPressTogglesWithoutSelection(t *testing.T) {
	t.Parallel()

	m, _ := wired(t)
	m = resize(m, 100, 40)
	m = deliver(t, m, golemclient.ToolCallStart{
		Type: golemclient.TypeToolCallStart, CallID: "c1", Tool: "read",
		Args: []byte(`{"path":"main.go"}`),
	})
	m = deliver(t, m, golemclient.ToolCallEnd{
		Type: golemclient.TypeToolCallEnd, CallID: "c1", OK: true,
		Result: "1: package main",
	})

	if len(m.toolSpans) != 1 {
		t.Fatalf("toolSpans = %+v, want one entry", m.toolSpans)
	}

	m = press(m, stickyTopRow(m)+m.toolSpans[0].start-m.vp.YOffset())

	if !m.blocks[0].expanded {
		t.Error("press on the tool row must still toggle expansion")
	}
	if m.selecting || m.selectedText() != "" {
		t.Error("press on the tool row must not start a text selection")
	}
}

func TestModel_SelectionSurvivesStreaming(t *testing.T) {
	t.Parallel()

	m := selectionTranscript(t)

	m = press(m, stickyTopRow(m)+2)
	m = dragMove(m, stickyTopRow(m)+4)

	m = deliver(t, m, golemclient.TurnStart{Type: golemclient.TypeTurnStart})
	m = deliver(t, m, golemclient.TextDelta{
		Type: golemclient.TypeTextDelta, Delta: "l10\nl11\nl12",
	})
	m = deliver(t, m, golemclient.TurnEnd{Type: golemclient.TypeTurnEnd})

	got := m.selectedText()
	if !strings.Contains(got, "l2") || !strings.Contains(got, "l4") || strings.Contains(got, "l10") {
		t.Errorf("streaming must not move the selection:\n%s", got)
	}
	if !strings.Contains(m.View().Content, "\x1b[7m") {
		t.Error("highlight must survive re-render during streaming")
	}
}

func TestModel_ReleaseEndsDragExtension(t *testing.T) {
	t.Parallel()

	m := selectionTranscript(t)

	m = press(m, stickyTopRow(m)+2)
	m = dragMove(m, stickyTopRow(m)+4)
	m = releaseMouse(m)
	m = dragMove(m, stickyTopRow(m)+6)

	if m.selecting {
		t.Error("release must end the drag")
	}
	if m.selHead != 4 {
		t.Errorf("head after post-release drag=%d; want 4 (frozen)", m.selHead)
	}
}

func TestReverseLine(t *testing.T) {
	t.Parallel()

	const (
		on  = "\x1b[7m"
		off = "\x1b[27m"
	)

	cases := []struct {
		name string
		in   string
		want string
	}{
		{"plain", "abc", on + "abc" + off},
		{"empty", "", on + off},
		{"reset reasserted", "a\x1b[0mb", on + "a\x1b[0m" + on + "b" + off},
		{"styled", "\x1b[31mred\x1b[0m tail", on + "\x1b[31mred\x1b[0m" + on + " tail" + off},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			if got := reverseLine(tc.in); got != tc.want {
				t.Errorf("reverseLine(%q) = %q; want %q", tc.in, got, tc.want)
			}
		})
	}
}
