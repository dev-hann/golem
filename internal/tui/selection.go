// Drag-to-select over the transcript: the mouse already reports to the
// app (wheel scrolling), so text selection is rendered in-app — reverse
// video over the selected content lines, copied to the system clipboard
// through the injected copier (pbcopy by default).

package tui

import (
	"context"
	"fmt"
	"os/exec"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/x/ansi"
)

// clipboardTimeout bounds the pbcopy exec so a stuck clipboard helper
// cannot park the cmd goroutine forever.
const clipboardTimeout = 2 * time.Second

// copyErrMsg reports a clipboard write failure back into the loop.
type copyErrMsg struct{ err error }

// copyToClipboard writes text to the system clipboard via pbcopy (macOS).
// It is the default copier; tests inject fakes.
func copyToClipboard(text string) error {
	ctx, cancel := context.WithTimeout(context.Background(), clipboardTimeout)
	defer cancel()

	cmd := exec.CommandContext(ctx, "pbcopy")
	cmd.Stdin = strings.NewReader(text)

	if err := cmd.Run(); err != nil {
		return fmt.Errorf("pbcopy: %w", err)
	}

	return nil
}

// copyText returns a cmd handing text to the clipboard through the
// injected copier (pbcopy by default). Failures come back as copyErrMsg.
func (m Model) copyText(text string) tea.Cmd {
	copier := m.copier
	if copier == nil {
		copier = copyToClipboard
	}

	return func() tea.Msg {
		if err := copier(text); err != nil {
			return copyErrMsg{err: err}
		}

		return nil
	}
}

// selectionRange returns the selected content-line range [lo, hi]
// inclusive; ok is false while nothing is selected.
func (m Model) selectionRange() (lo, hi int, ok bool) {
	if m.selAnchor == m.selHead {
		return 0, 0, false
	}

	return min(m.selAnchor, m.selHead), max(m.selAnchor, m.selHead), true
}

// selectedText returns the selection as plain text, styles stripped.
func (m Model) selectedText() string {
	lo, hi, ok := m.selectionRange()
	if !ok {
		return ""
	}

	lines := strings.Split(m.content, "\n")
	lo, hi = max(lo, 0), min(hi, len(lines)-1)

	plain := make([]string, 0, hi-lo+1)
	for i := lo; i <= hi; i++ {
		plain = append(plain, ansi.Strip(lines[i]))
	}

	return strings.Join(plain, "\n")
}

// Reverse-video SGR sequences for the selection highlight.
const (
	reverseOn  = "\x1b[7m"
	reverseOff = "\x1b[27m"
)

// highlight overlays reverse video on the selected transcript lines,
// leaving the rest of the content untouched.
func (m Model) highlight(content string) string {
	lo, hi, ok := m.selectionRange()
	if !ok {
		return content
	}

	lines := strings.Split(content, "\n")
	lo, hi = max(lo, 0), min(hi, len(lines)-1)

	for i := lo; i <= hi; i++ {
		lines[i] = reverseLine(lines[i])
	}

	return strings.Join(lines, "\n")
}

// reverseLine wraps one rendered line in reverse video, reasserting it
// after every embedded SGR reset so kit styles cannot switch the
// highlight off mid-line.
func reverseLine(line string) string {
	return reverseOn + strings.ReplaceAll(line, "\x1b[0m", "\x1b[0m"+reverseOn) + reverseOff
}

// beginSelection starts a drag on the content line under the screen
// row, replacing any retained selection. Presses that hit tool rows,
// the sticky bar, the composer, or modal backdrops never start one.
func (m *Model) beginSelection(y int) {
	if m.mode != modeChat {
		return
	}

	line, ok := m.lineAt(y)
	if !ok {
		return
	}

	m.selecting = true
	m.selAnchor, m.selHead = line, line
	m.refreshView()
}

// extendSelection moves the drag's head to the screen row, clamped to
// the visible transcript.
func (m *Model) extendSelection(y int) {
	if !m.selecting || m.mode != modeChat {
		return
	}

	m.selHead = m.lineClamped(y)
	m.refreshView()
}

// endSelection finishes the drag; a zero-width drag (a plain click)
// retains nothing.
func (m *Model) endSelection() {
	m.selecting = false
}

// clearSelection drops the selection and its highlight state.
func (m *Model) clearSelection() {
	m.selecting = false
	m.selAnchor, m.selHead = 0, 0
}

// lineAt maps a screen row to the transcript content line under it;
// ok is false for the sticky bar, the composer, and off-screen rows.
func (m Model) lineAt(y int) (int, bool) {
	top := stickyTopRow(m)
	if y < top || y >= top+m.vp.Height {
		return 0, false
	}

	return m.vp.YOffset + y - top, true
}

// lineClamped maps a screen row to a content line, clamped into the
// visible transcript for drags pulled past its edges.
func (m Model) lineClamped(y int) int {
	top := stickyTopRow(m)
	y = min(max(y, top), top+m.vp.Height-1)

	return min(m.vp.YOffset+y-top, len(strings.Split(m.content, "\n"))-1)
}

// stickyTopRow reports the screen row the transcript starts on: one
// below the sticky bar while it is pinned, else the top row.
func stickyTopRow(m Model) int {
	if m.stickyText() != "" {
		return 1
	}

	return 0
}
