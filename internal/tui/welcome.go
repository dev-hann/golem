// The welcome screen: a centered ASCII banner plus quick-start hints
// shown while the transcript is empty and no session exists (the
// pre-first-message state, ADR-0018). The first submitted message or a
// loaded session replaces it — it is pure view state, nothing here
// touches the model's session bookkeeping.

package tui

import (
	"strings"

	"github.com/charmbracelet/x/ansi"

	"github.com/dev-hann/golem/internal/tui/kit"
)

// welcomeArt is the GOLEM banner: figlet "standard" letterforms (G·O·L·E·M
// with kerned single-space joins), every row padded to the exact same cell
// width (pinned by test) — the uniform width is what horizontal centering
// against the viewport measures.
const (
	welcomeArtWidth = 41
	welcomeArtRows  = 5
)

var welcomeArt = [welcomeArtRows]string{
	"  ____     ___    _       _____   __  __ ",
	" / ___|   / _ \\  | |     | ____| |  \\/  |",
	"| |  _   | | | | | |     |  _|   | |\\/| |",
	"| |_| |  | |_| | | |___  | |___  | |  | |",
	" \\____|   \\___/  |_____| |_____| |_|  |_|",
}

// welcomeCaptions are the hint lines under the banner: what golem is,
// how to start, and the three keys worth knowing on day one.
var welcomeCaptions = []string{
	"golem · a terminal agent",
	"type a message to start a session",
	"ctrl+p commands · ctrl+x n new · ctrl+x l sessions · ctrl+x m model",
}

// showWelcome reports whether the welcome screen owns the transcript
// region: no session yet, nothing pending, and an empty transcript —
// exactly the fresh-launch state before the first word (ADR-0018).
func (m Model) showWelcome() bool {
	return m.sessionID == "" && len(m.blocks) == 0 && len(m.pendingMsgs) == 0
}

// welcomeView renders the centered banner block sized to the viewport:
// exactly vp.Height rows (the layout's exact-height invariant), each row
// no wider than vp.Width. Content that cannot fit a floor-height
// viewport truncates from the bottom — tiny terminals keep the banner
// and lose the hints, never the layout.
func (m Model) welcomeView() string {
	rows := make([]string, 0, len(welcomeArt)+1+len(welcomeCaptions))

	for _, line := range welcomeArt {
		rows = append(rows, m.kit.Style(kit.Title).Render(line))
	}

	rows = append(rows, "")

	for _, caption := range welcomeCaptions {
		rows = append(rows, m.kit.Style(kit.Dim).Render(caption))
	}

	for i, row := range rows {
		rows[i] = centerRow(row, m.vp.Width)
	}

	height := max(m.vp.Height, viewportMinHeight)

	out := make([]string, 0, height)
	for range max((height-len(rows))/2, 0) {
		out = append(out, "")
	}

	out = append(out, rows...)

	for len(out) < height {
		out = append(out, "")
	}

	return strings.Join(out[:min(len(out), height)], "\n")
}

// centerRow insets a styled row to the horizontal center of width,
// tolerating rows wider than the terminal (no inset, truncated to fit).
func centerRow(row string, width int) string {
	inset := max((width-ansi.StringWidth(row))/2, 0)

	return ansi.Truncate(strings.Repeat(" ", inset)+row, max(width, 1), "")
}
