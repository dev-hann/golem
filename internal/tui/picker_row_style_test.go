package tui

import (
	"strings"
	"testing"
	"time"

	"github.com/dev-hann/golem/internal/tui/kit"
	"github.com/dev-hann/golem/pkg/golemclient"
)

// forcedColorKit builds the dark-variant kit: style emission carries
// the theme's full-fidelity colors (v2 renders them all; downsampling
// is a write-time concern).
func forcedColorKit() *kit.Kit {
	return kit.New(kit.DefaultTheme())
}

// TestModel_FocusedPickerRowRendersPlain pins the focus/style clash: a
// focused row is wrapped in kit.Row's reverse video, and any nested
// color span inside it has its foreground turned into a background —
// the current-session bar became a bright block, the spinner and age
// colored rectangles. Focused rows must therefore carry no ANSI styles
// of their own; the glyphs alone carry the meaning, and color returns
// once the cursor moves off.
func TestModel_FocusedPickerRowRendersPlain(t *testing.T) {
	t.Parallel()

	m := NewThemed("/dev/null", forcedColorKit())
	m.sessionID = "s1"
	m.turnActive = true // the current row is also the running one

	now := time.Now()
	s := golemclient.SessionSummary{
		ID: "s1", Title: "current", Cwd: "/w",
		LastActive: now.Add(-5 * time.Minute).Format(time.RFC3339),
	}

	focused := m.sessionRow(s, now, 80, true)

	if strings.Contains(focused, "\x1b[") {
		t.Errorf("focused row must render plain — nested styles double-render under reverse video: %q", focused)
	}

	for _, want := range []string{"▎", "current", "5m ago"} {
		if !strings.Contains(focused, want) {
			t.Errorf("focused row lost %q: %q", want, focused)
		}
	}

	statusCol := strings.TrimPrefix(focused, "▎ ")[:4]
	if !strings.ContainsAny(statusCol, "⠋⠙⠹⠸⠼⠴⠦⠧⠇⠏") {
		t.Errorf("focused running row must keep the spinner glyph: %q", focused)
	}

	// Unfocused rows keep the styling: bar in the User token, spinner
	// in Accent, age dimmed.
	unfocused := m.sessionRow(s, now, 80, false)

	if !strings.Contains(unfocused, "▎") || !strings.Contains(unfocused, "\x1b[") {
		t.Errorf("unfocused current row keeps the bar glyph and its color: %q", unfocused)
	}

	// A focused row that is neither current nor running stays plain too.
	other := golemclient.SessionSummary{
		ID: "s2", Title: "other", Cwd: "/w",
		LastActive: now.Add(-5 * time.Minute).Format(time.RFC3339),
	}
	if got := m.sessionRow(other, now, 80, true); strings.Contains(got, "\x1b[") {
		t.Errorf("focused foreign row must render plain: %q", got)
	}
}

// TestModel_FocusedDeleteRowRendersPlain extends the same contract to
// the armed delete row — it is by construction the cursor row, so its
// error-colored hint would sit reversed inside the highlight.
func TestModel_FocusedDeleteRowRendersPlain(t *testing.T) {
	t.Parallel()

	m := NewThemed("/dev/null", forcedColorKit())
	m.sessDeleteArmed = "s1"

	s := golemclient.SessionSummary{ID: "s1", Title: "current", Cwd: "/w"}

	if got := m.sessDeleteRow(s, 80, true); strings.Contains(got, "\x1b[") {
		t.Errorf("focused armed row must render plain: %q", got)
	}

	if got := m.sessDeleteRow(s, 80, false); !strings.Contains(got, "\x1b[") {
		t.Errorf("unfocused armed row keeps the error-colored hint: %q", got)
	}
}
