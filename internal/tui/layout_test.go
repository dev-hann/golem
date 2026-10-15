package tui

import (
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"

	"github.com/charmbracelet/x/ansi"

	"github.com/dev-hann/golem/pkg/golemclient"
)

// paddedChat builds a connected model at 80x24 with a short transcript,
// the base case for layout geometry tests.
func paddedChat(t *testing.T) Model {
	t.Helper()

	m, _ := wired(t)
	m = resize(m, 80, 24)
	m = deliver(t, m, golemclient.TurnStart{Type: golemclient.TypeTurnStart})
	m = deliver(t, m, golemclient.TextDelta{
		Type:  golemclient.TypeTextDelta,
		Delta: "hello padding",
	})
	m = deliver(t, m, golemclient.TurnEnd{Type: golemclient.TypeTurnEnd})

	return m
}

func TestLayout_HasScreenPadding(t *testing.T) {
	t.Parallel()

	m := paddedChat(t)

	lines := strings.Split(m.View().Content, "\n")

	// Top padding row: blank.
	if strings.TrimSpace(lines[0]) != "" {
		t.Errorf("first screen row must be blank padding, got %q", lines[0])
	}

	// Every row starts with the left pad.
	for i, line := range lines {
		if !strings.HasPrefix(line, strings.Repeat(" ", screenPadH)) {
			t.Errorf("row %d must start with %d pad columns, got %q", i, screenPadH, line)
		}
	}

	// Bottom padding row: blank and last.
	if last := lines[len(lines)-1]; strings.TrimSpace(last) != "" {
		t.Errorf("last screen row must be blank padding, got %q", last)
	}
}

func TestLayout_PadsHorizontalOnlyContentRows(t *testing.T) {
	t.Parallel()

	m := paddedChat(t)

	lines := strings.Split(m.View().Content, "\n")

	// The transcript row sits one row below the header and carries the
	// pad prefix.
	row := lines[screenPadV+headerRows]
	content := strings.TrimPrefix(row, strings.Repeat(" ", screenPadH))

	if !strings.Contains(content, "hello padding") {
		t.Errorf("transcript row missing text after pad: %q", row)
	}
}

func TestResize_AccountsForPadding(t *testing.T) {
	t.Parallel()

	m, _ := wired(t)
	m = resize(m, 80, 24)

	if want := max(80-2*screenPadH, viewportMinWidth); m.vp.Width() != want {
		t.Errorf("viewport width = %d, want %d (terminal minus side pads)", m.vp.Width(), want)
	}

	// The composer frame (border included) must fit between the side
	// pads. The textarea's Width() reports an inner width — its prompt
	// claims 2 cells — so measure the rendered box instead.
	for _, line := range strings.Split(m.View().Content, "\n") {
		if w := ansi.StringWidth(ansi.Strip(line)); w > 80 {
			t.Errorf("screen row %d cells wide, exceeds terminal 80: %q", w, line)
		}
	}
}

func TestLayout_FillsExactHeight(t *testing.T) {
	t.Parallel()

	m := paddedChat(t)

	if got := strings.Count(m.View().Content, "\n") + 1; got != m.height {
		t.Errorf("screen height = %d rows, want exactly %d", got, m.height)
	}
}

func TestLayout_ComposerSitsBelowGap(t *testing.T) {
	t.Parallel()

	m := paddedChat(t)

	lines := strings.Split(m.View().Content, "\n")

	// Find the composer's top border row (rounded border corners).
	composerTop := -1

	for i, line := range lines {
		stripped := ansi.Strip(line)

		if len(stripped) > screenPadH && strings.HasPrefix(stripped[screenPadH:], "╭") {
			composerTop = i

			break
		}
	}

	if composerTop < 0 {
		t.Fatalf("composer box not found in view:\n%s", m.View().Content)
	}

	// The row above the composer is the blank gap.
	gap := ansi.Strip(lines[composerTop-1])
	if strings.TrimSpace(gap) != "" {
		t.Errorf("row above composer must be the blank gap, got %q", gap)
	}
}

func TestLayout_SmallTerminalCollapsesPadding(t *testing.T) {
	t.Parallel()

	m, _ := wired(t)
	m = resize(m, 24, 10)

	if got := strings.Count(m.View().Content, "\n") + 1; got != m.height {
		t.Errorf("small terminal screen height = %d, want %d", got, m.height)
	}

	// Viewport keeps its floor even with pads squeezing it.
	if m.vp.Width() < viewportMinWidth {
		t.Errorf("viewport width = %d, below floor %d", m.vp.Width(), viewportMinWidth)
	}

	if m.vp.Height() < viewportMinHeight {
		t.Errorf("viewport height = %d, below floor %d", m.vp.Height(), viewportMinHeight)
	}
}

func TestSelection_TopPaddingRowsIgnored(t *testing.T) {
	t.Parallel()

	m := selectionTranscript(t)

	// A press on the top padding row starts nothing.
	next := press(m, screenPadV-1)
	if next.selecting {
		t.Error("press on the top padding row must not begin a selection")
	}

	// A press on the first transcript row does.
	next = press(m, stickyTopRow(m))
	if !next.selecting {
		t.Error("press on the first transcript row must begin a selection")
	}
}

func TestSelection_LeftPadColumnsIgnored(t *testing.T) {
	t.Parallel()

	m := selectionTranscript(t)

	// A press whose x lands on the left pad still selects the row under
	// it — rows gate selection, x never does.
	next, _ := m.Update(tea.MouseClickMsg{Button: tea.MouseLeft, X: screenPadH, Y: stickyTopRow(m)})
	if !next.(Model).selecting {
		t.Error("press on the first transcript row (x on the pad) must select")
	}
}
