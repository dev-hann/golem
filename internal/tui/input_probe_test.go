package tui

import (
	"strings"
	"testing"

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

// probeTextRows counts the rendered lines that carry content: the textarea
// pads its view with end-of-buffer lines, each containing the NUL marker,
// so every line without it is a content row.
func probeTextRows(view string) int {
	rows := 0

	for _, line := range strings.Split(view, "\n") {
		if !strings.ContainsRune(ansi.Strip(line), composerEOBChar) {
			rows++
		}
	}

	return rows
}

// clampedProbeRows applies syncInputHeight's terminal-share clamp to the
// probe's answer, reproducing what inputRows should be under the old
// measurement for a given terminal height. The probe is fed the OUTER
// width — what the model passes to SetWidth. The pre-8.1 production code
// fed it the textarea's content width instead, double-subtracting the
// prompt; see TestInput_HeightGrowsWithSoftWrap's hangul note.
func clampedProbeRows(value string, outerWidth, termHeight int) int {
	if termHeight == 0 {
		termHeight = startHeight
	}

	probe := textarea.New()
	probe.ShowLineNumbers = false
	probe.EndOfBufferCharacter = composerEOBChar
	probe.SetWidth(outerWidth)

	rows := 0
	for h := composerProbeHeight; ; h *= 2 {
		probe.SetHeight(h)
		probe.SetValue(value)

		if n := probeTextRows(probe.View()); n < h {
			rows = max(n, minInputHeight)
			break
		}
	}

	rowCap := max(termHeight*inputMaxShareNumerator/inputMaxShareDenominator, minInputHeight)

	return min(max(rows, minInputHeight), rowCap)
}

// TestInput_DynamicHeightMatchesProbe pins Phase 8.1's replacement — the
// composer auto-sizing via textarea.DynamicHeight — against the probe it
// deleted. Every case must answer what the probe would, clamped by the
// terminal share: wrap parity is the contract (v1 and v2 wrap differences
// show up here first, per ROADMAP 8.1 acceptance criterion 2).
func TestInput_DynamicHeightMatchesProbe(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name   string
		width  int
		height int
		value  string
	}{
		{"empty input", 40, 30, ""},
		{"single line", 40, 30, "hello"},
		{"hard newlines", 40, 30, "one\ntwo\nthree\nfour"},
		{"soft wrapped", 40, 30, strings.Repeat("a", 100)},
		{"hangul soft wrap", 24, 30, strings.Repeat("가", 40)},
		{"word boundary moves whole words", 12, 30, "aaaaaaaaaa bbbbbbbbbb"},
		{"explicit newline plus wrap", 12, 30, "aaaa\n" + strings.Repeat("b", 30)},
		{"trailing space anchors a row", 22, 30, strings.Repeat("a", 17) + " "},
		{"width filled exactly", 22, 30, "word " + strings.Repeat("x", 13)},
		{"word run at a wider boundary", 40, 30, "aaaa bbbb cccc dddd eeee ffff gggg"},
		{"over the share cap", 80, 10, strings.Repeat("a", 400)},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			m := New("/tmp/unused.sock")
			m.resize(tt.width, tt.height)
			m.input.SetValue(tt.value)
			m.syncInputHeight()

			if !m.input.DynamicHeight {
				t.Fatal("composer textarea must run with DynamicHeight enabled")
			}

			// The outer width is what resize feeds SetWidth (terminal minus
			// screen padding and the box border); Width() reports the content
			// width after the prompt is subtracted inside.
			outer := max(tt.width-2*screenPadH-inputBorderCols, viewportMinWidth)
			want := clampedProbeRows(tt.value, outer, tt.height)
			if m.inputRows != want {
				t.Errorf("inputRows = %d, want probe answer %d (value %q, textarea width %d, terminal %dx%d)",
					m.inputRows, want, tt.value, m.input.Width(), tt.width, tt.height)
			}
		})
	}
}

// TestInput_HeightCapStillFeedsViewportFloor verifies the terminal-share
// cap survives the DynamicHeight switch: an input far past the cap clamps
// the box, and the transcript viewport keeps its minimum rows.
func TestInput_HeightCapStillFeedsViewportFloor(t *testing.T) {
	t.Parallel()

	m := New("/tmp/unused.sock")
	m.resize(80, 10) // share cap: 10 * 2/5 = 4 rows

	m.input.InsertString(strings.Repeat("line\n", 20)) // 21 rows, far over the cap
	m.syncInputHeight()

	if want := 10 * inputMaxShareNumerator / inputMaxShareDenominator; m.inputRows != want {
		t.Errorf("inputRows = %d, want capped at %d", m.inputRows, want)
	}

	if m.vp.Height() < viewportMinHeight {
		t.Errorf("viewport height = %d, below floor", m.vp.Height())
	}
}
