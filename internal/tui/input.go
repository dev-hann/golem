package tui

import (
	"strings"

	"charm.land/bubbles/v2/textarea"
	"github.com/charmbracelet/x/ansi"
)

// composerProbeHeight is the starting height of the throwaway textarea the
// composer row measurement renders into. It doubles whenever the value
// proves longer than the probe is tall, so any input terminates the loop.
const composerProbeHeight = 64

// composerEOBChar replaces the textarea's "~" end-of-buffer marker in the
// probe so padding rows are unambiguous: content rows can never contain a
// NUL rune, while every padding row does.
const composerEOBChar = '\x00'

// composerRows reports how many screen rows the composer's content occupies
// once soft-wrapped at the given width. Instead of reimplementing the wrap,
// it asks the textarea itself: a throwaway probe renders the value tall
// enough to avoid internal scrolling, and we count the rows that carry
// content. Parity matters — an undercount keeps the box shorter than its
// content and the viewport hides the top rows. bubbles wraps lines that
// fill the width one row earlier than a plain width split suggests
// (trailing-space anchors, inclusive boundaries), which pure width math
// cannot reproduce.
func composerRows(value string, width int) int {
	if value == "" {
		return minInputHeight
	}

	probe := textarea.New()
	probe.ShowLineNumbers = false
	probe.EndOfBufferCharacter = composerEOBChar
	probe.SetWidth(width)

	for h := composerProbeHeight; ; h *= 2 {
		probe.SetHeight(h)
		probe.SetValue(value)

		if n := probeTextRows(probe.View()); n < h {
			return max(n, minInputHeight)
		}
	}
}

// probeTextRows counts the rendered lines that carry content: the textarea
// pads its view with end-of-buffer lines, each containing the NUL marker,
// so every line without it is a content row — including rows that only hold
// an empty logical line.
func probeTextRows(view string) int {
	rows := 0

	for _, line := range strings.Split(view, "\n") {
		if !strings.ContainsRune(ansi.Strip(line), composerEOBChar) {
			rows++
		}
	}

	return rows
}
