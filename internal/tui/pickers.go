// The two modal pickers — sessions (ctrl+x l) and models (ctrl+x m) —
// their key handling, list building, and row rendering.

package tui

import (
	"fmt"
	"path/filepath"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/x/ansi"

	"github.com/dev-hann/golem/internal/tui/kit"
	"github.com/dev-hann/golem/pkg/golemclient"
)

// updateModels drives the model picker modal: arrows move, enter applies
// via set_model, esc cancels.
func (m Model) updateModels(key tea.KeyMsg) (tea.Model, tea.Cmd) {
	if len(m.models) == 0 {
		m.mode = modeChat

		return m, nil
	}

	switch key.Type { //nolint:exhaustive // foreign enum; default ignores
	case tea.KeyUp, tea.KeyCtrlP:
		m.modelCursor = max(m.modelCursor-1, 0)
	case tea.KeyDown, tea.KeyCtrlN:
		m.modelCursor = min(m.modelCursor+1, len(m.models)-1)
	case tea.KeyEsc:
		m.mode = modeChat
	case tea.KeyEnter:
		chosen := m.models[m.modelCursor]
		m.modelName = chosen
		m.mode = modeChat

		// No session yet (ADR-0018): remember the choice and apply it
		// when session_meta arrives.
		if m.sessionID == "" {
			m.pendingModel = chosen
			return m, nil
		}

		return m, m.send(golemclient.SetModel{
			Type: golemclient.TypeSetModel, Session: m.sessionID, Model: chosen,
		})
	default:
	}

	return m, nil
}

// cursorOnModel prepositions the picker cursor on the current model.
func (m Model) cursorOnModel(current string) int {
	for i, model := range m.models {
		if model == current {
			return i
		}
	}

	return 0
}

// WithModels supplies the provider's model list for the ctrl+m picker.
func (m Model) WithModels(models []string) Model {
	m.models = models

	return m
}

// updateSessions drives the session picker: typing filters (fuzzy over
// title and directory), ctrl+r renames the highlighted row inline,
// ctrl+d deletes it (press twice to confirm — opencode's pattern), and
// the dialog keys move/confirm. esc clears the query first, then closes.
// Every cursor or filter change funnels through one trailing
// scrollToCursor so the window tracks the cursor.
func (m Model) updateSessions(key tea.KeyMsg) (tea.Model, tea.Cmd) {
	if m.sessRenaming {
		return m.renameKey(key)
	}

	filtered := m.filteredSessions()

	switch key.Type { //nolint:exhaustive // foreign enum; default filters
	case tea.KeyEsc:
		m.sessDeleteArmed = ""

		if m.sessQuery != "" {
			m.sessQuery = ""
			m.pickIndex = 0

			return m, nil
		}

		m.mode = modeChat

		return m, nil
	case tea.KeyCtrlR:
		if len(filtered) == 0 {
			return m, nil
		}

		m.sessRenaming = true
		m.sessRenameText = filtered[m.pickIndex].Title

		return m, nil
	case tea.KeyCtrlD:
		return m.deleteKey(filtered)
	case tea.KeyUp, tea.KeyCtrlP:
		m.sessDeleteArmed = ""
		m.pickIndex = max(m.pickIndex-1, 0)
	case tea.KeyDown, tea.KeyCtrlN:
		m.sessDeleteArmed = ""
		m.pickIndex = min(m.pickIndex+1, len(filtered)-1)
	case tea.KeyBackspace:
		m.sessDeleteArmed = ""

		if runes := []rune(m.sessQuery); len(runes) > 0 {
			m.sessQuery = string(runes[:len(runes)-1])
			m.pickIndex = min(m.pickIndex, max(len(m.filteredSessions())-1, 0))
		}
	case tea.KeyEnter:
		if len(filtered) == 0 {
			return m, nil
		}

		chosen := filtered[m.pickIndex]
		m.mode = modeChat
		m.sessDeleteArmed = ""
		m.clearTranscript()

		return m, m.send(golemclient.LoadSession{
			Type: golemclient.TypeLoadSession, Session: chosen.ID,
		})
	case tea.KeyRunes:
		if !key.Alt {
			m.sessDeleteArmed = ""
			m.sessQuery += string(key.Runes)
			m.pickIndex = min(m.pickIndex, max(len(m.filteredSessions())-1, 0))
		}
	default:
		return m, nil
	}

	m.scrollToCursor()

	return m, nil
}

// removeSession drops a session from the picker list, clamping the
// cursor and scroll back into range.
func (m *Model) removeSession(id string) {
	out := m.sessions[:0:0]
	for _, s := range m.sessions {
		if s.ID != id {
			out = append(out, s)
		}
	}

	m.sessions = out
	m.pickIndex = min(m.pickIndex, max(len(m.filteredSessions())-1, 0))
	m.scrollToCursor()
}

// deleteKey handles the picker's ctrl+d: the first press arms the
// highlighted row, the second deletes it optimistically (opencode
// removes the row immediately) and lets session_deleted reconcile.
func (m Model) deleteKey(filtered []golemclient.SessionSummary) (tea.Model, tea.Cmd) {
	if len(filtered) == 0 {
		return m, nil
	}

	chosen := filtered[m.pickIndex]

	if m.sessDeleteArmed != chosen.ID {
		m.sessDeleteArmed = chosen.ID

		return m, nil
	}

	m.sessDeleteArmed = ""
	m.removeSession(chosen.ID)

	if m.sessionID == chosen.ID { // deleted the session we stand in
		m.clearTranscript()
		m.sessionID = ""
	}

	return m, m.send(golemclient.DeleteSession{
		Type: golemclient.TypeDeleteSession, Session: chosen.ID,
	})
}

// renameKey edits the highlighted session's title: typing and backspace
// edit, ctrl+u clears, enter commits (optimistically — the server's
// session_title reply reconciles), esc cancels.
func (m Model) renameKey(key tea.KeyMsg) (tea.Model, tea.Cmd) {
	filtered := m.filteredSessions()

	commit := func() (tea.Model, tea.Cmd) {
		chosen := filtered[m.pickIndex]

		title := strings.TrimSpace(m.sessRenameText)
		if title == "" {
			m.sessRenaming = false
			m.sessRenameText = ""

			return m, nil
		}

		m.sessRenaming = false
		m.sessRenameText = ""

		if i := chosenIndex(m.sessions, chosen.ID); i >= 0 {
			m.sessions[i].Title = title
		}

		if chosen.ID == m.sessionID {
			m.sessionTitle = title
		}

		return m, m.send(golemclient.RenameSession{
			Type: golemclient.TypeRenameSession, Session: chosen.ID, Title: title,
		})
	}

	switch key.Type { //nolint:exhaustive // foreign enum; default ignores
	case tea.KeyEscape:
		m.sessRenaming = false
		m.sessRenameText = ""

		return m, nil
	case tea.KeyEnter:
		return commit()
	case tea.KeyCtrlU:
		m.sessRenameText = ""

		return m, nil
	case tea.KeyBackspace:
		if runes := []rune(m.sessRenameText); len(runes) > 0 {
			m.sessRenameText = string(runes[:len(runes)-1])
		}

		return m, nil
	case tea.KeyRunes:
		if !key.Alt {
			m.sessRenameText += string(key.Runes)
		}

		return m, nil
	default:
		return m, nil
	}
}

// filteredSessions returns the rows matching the picker's search query.
func (m Model) filteredSessions() []golemclient.SessionSummary {
	if m.sessQuery == "" {
		return m.sessions
	}

	out := make([]golemclient.SessionSummary, 0, len(m.sessions))

	for _, s := range m.sessions {
		if fuzzyContains(m.sessQuery, s.Title+" "+dirName(s.Cwd)) {
			out = append(out, s)
		}
	}

	return out
}

// chosenIndex locates a session id in the full list; -1 when absent.
func chosenIndex(sessions []golemclient.SessionSummary, id string) int {
	for i := range sessions {
		if sessions[i].ID == id {
			return i
		}
	}

	return -1
}

// anyRunning reports whether any listed session has a live turn — the
// cue to spend ticks on the picker's spinner.
func anyRunning(m *Model) bool {
	for _, s := range m.sessions {
		if m.sessionRunning(s) {
			return true
		}
	}

	return false
}

// cursorOnSession prepositions the picker cursor on the current session;
// 0 when it is absent from the list.
func (m *Model) cursorOnSession() int {
	for i := range m.sessions {
		if m.sessions[i].ID == m.sessionID {
			return i
		}
	}

	return 0
}

// modelsBody renders the model picker list.
func (m Model) modelsBody() string {
	if len(m.models) == 0 {
		current := m.modelName
		if current == "" {
			current = "unknown"
		}

		return current + "\n" + m.kit.DimLine("no model list — enter closes")
	}

	var b strings.Builder

	for i, model := range m.models {
		fmt.Fprintf(&b, "%s\n", m.kit.Row(model, i == m.modelCursor)) //nolint:errcheck // strings.Builder
	}

	b.WriteString(m.kit.DimLine("↑↓ select · enter apply · esc cancel"))

	return b.String()
}

// sessionsBody renders the session picker: a scrollable window over
// date-grouped rows (opencode's shape — Today/Yesterday/… headers),
// the live query, and the key hints. Rows carry a · current marker for
// the session this client is in and · running for sessions with a live
// turn. The window is capped so the modal never outgrows the screen;
// the cursor (or the mouse wheel) drives the scroll.
func (m Model) sessionsBody() string {
	var b strings.Builder

	lines := m.pickerLines()

	if len(lines) == 0 {
		b.WriteString(m.kit.DimLine("no matches") + "\n")
	}

	for _, ln := range lines {
		fmt.Fprintf(&b, "%s\n", ln.render) //nolint:errcheck // strings.Builder
	}

	// The search field: without it nothing shows what the user typed —
	// rows narrowing is the only feedback, and a typo has no visible
	// correction. Mirrors the command palette's prompt line.
	b.WriteString("\n" + m.kit.Prompt("› "+m.sessQuery+"_") + "\n")
	b.WriteString(m.kit.DimLine("type to filter · ctrl+r rename · ctrl+d delete · enter open · esc cancel"))

	return b.String()
}

// pickerLine is one rendered line of the picker list: either a date
// group header or a session row.
type pickerListLine struct {
	render    string
	sessionID string // "" for headers
	rowIndex  int    // index into filteredSessions; -1 for headers
}

// pickerLines builds the flat line list (headers + rows) and windows it
// to the visible budget around the stored scroll. Pure: the scroll
// itself moves only in update paths (scrollToCursor, the wheel).
func (m Model) pickerLines() []pickerListLine {
	filtered := m.filteredSessions()
	now := time.Now()

	var lines []pickerListLine

	lastGroup := ""
	for i, s := range filtered {
		if label, ok := dateGroupLabel(now, s.LastActive); ok && label != lastGroup {
			lines = append(lines, pickerListLine{
				render: m.kit.DimLine(label), sessionID: "", rowIndex: -1,
			})
			lastGroup = label
		}

		row := m.sessionRow(s, now, m.sessRowWidth(), i == m.pickIndex)

		if m.sessDeleteArmed == s.ID {
			row = m.sessDeleteRow(s, m.sessRowWidth(), i == m.pickIndex)
		}

		if i == m.pickIndex && m.sessRenaming {
			row = m.kit.Prompt("› " + m.sessRenameText + "_")
		}

		lines = append(lines, pickerListLine{
			render: m.kit.Row(row, i == m.pickIndex), sessionID: s.ID, rowIndex: i,
		})
	}

	start := min(max(m.sessScroll, 0), max(len(lines)-m.pickerVisible(), 0))
	end := min(start+m.pickerVisible(), len(lines))

	return lines[start:end]
}

// scrollPicker moves the picker's window freely (the wheel does not
// move the cursor) and clamps to the line range.
func (m *Model) scrollPicker(delta int) {
	total := m.pickerLineCount()
	m.sessScroll = min(max(m.sessScroll+delta, 0), max(total-m.pickerVisible(), 0))
}

// pickerLineCount is the flat line count the picker would render:
// session rows plus one header per date-group boundary.
func (m Model) pickerLineCount() int {
	filtered := m.filteredSessions()
	if len(filtered) == 0 {
		return 0
	}

	now := time.Now()
	lines := 0
	lastGroup := ""

	for _, s := range filtered {
		if label, ok := dateGroupLabel(now, s.LastActive); ok && label != lastGroup {
			lines++
			lastGroup = label
		}

		lines++
	}

	return lines
}

// scrollToCursor recenters the picker window so the cursor's line
// (header lines included) is visible — called wherever pickIndex moves
// or the list shrinks (delete, filter). Mirrors pickerLines' grouping
// exactly: a header line precedes a row whenever its group label
// differs from the previous row's (including the first row, whose
// implicit predecessor is no group at all).
func (m *Model) scrollToCursor() {
	filtered := m.filteredSessions()
	if len(filtered) == 0 {
		m.sessScroll = 0

		return
	}

	now := time.Now()

	lines := 0
	cursorLine := 0
	groupTop := 0 // the cursor's group header line (or its own row)
	lastGroup := ""
	headered := false

	for i, s := range filtered {
		headered = false

		if label, ok := dateGroupLabel(now, s.LastActive); ok && label != lastGroup {
			lines++
			lastGroup = label
			headered = true
		}

		if i == m.pickIndex {
			cursorLine = lines

			// The group's header sits one line above its first row
			// whenever one was emitted for this group; scrolling up
			// lands there so a group enters view with its label.
			if headered {
				groupTop = lines - 1
			} else {
				groupTop = lines
			}
		}

		lines++
	}

	switch {
	case cursorLine < m.sessScroll:
		m.sessScroll = groupTop
	case cursorLine >= m.sessScroll+m.pickerVisible():
		m.sessScroll = cursorLine - m.pickerVisible() + 1
	}

	m.sessScroll = min(max(m.sessScroll, 0), max(lines-m.pickerVisible(), 0))
}

// pickerVisible is the line budget for the picker window, clamped to
// the terminal: the modal (title + query + hints + frame) needs room,
// and a short terminal shrinks the window before the modal overflows.
func (m Model) pickerVisible() int {
	// Modal chrome: title row, blank row, query row, hints row, plus
	// the border/padding frame rows.
	const modalFixedRows = 8

	return max(min(pickerVisibleRows, m.height-modalFixedRows), 1)
}

// dateGroupLabel buckets a session's last-active time into a section
// label: Today, Yesterday, then the age (3d ago…) or the calendar date
// for anything older than a week — relTime's buckets, section-sized.
// Sessions missing a parsable timestamp join no group (ok=false) and
// render headerless, keeping the newest-first flow intact.
func dateGroupLabel(now time.Time, rfc3339 string) (string, bool) {
	ts, err := time.Parse(time.RFC3339, rfc3339)
	if err != nil {
		return "", false
	}

	diff := now.Sub(ts)

	switch {
	case diff < 24*time.Hour && now.Day() == ts.Day():
		return "Today", true
	case diff < 48*time.Hour:
		return "Yesterday", true
	case diff < 7*24*time.Hour:
		return fmt.Sprintf("%dd ago", int(diff.Hours()/24)), true
	default:
		return ts.Format("Jan 2"), true
	}
}

// rowBar renders the bar column: the current-session marker, plain
// when focused (reverse video inverts nested colors into blocks).
func (m Model) rowBar(sessionID string, focused bool) string {
	if sessionID != m.sessionID {
		return strings.Repeat(" ", sessBarCols)
	}

	if focused {
		return "▎ "
	}

	return m.kit.Marker(kit.User, "▎ ")
}

// rowStatus renders the status column: the running-turn spinner, plain
// when focused.
func (m Model) rowStatus(s golemclient.SessionSummary, focused bool) string {
	if !m.sessionRunning(s) {
		return strings.Repeat(" ", sessStatusCols)
	}

	glyph := kit.SpinnerFrames[(m.spinFrame%len(kit.SpinnerFrames)+len(kit.SpinnerFrames))%len(kit.SpinnerFrames)]

	if focused {
		return glyph + " "
	}

	return m.kit.Spinner(m.spinFrame) + " "
}

// sessDeleteHint is the armed row's confirm prompt.
const sessDeleteHint = "ctrl+d again to confirm"

// sessDeleteRow renders the armed row: same bar and status columns, the
// title replaced by the confirm hint in the error token — one
// destructive cue per opencode's press-again pattern, columns aligned.
// focused rows render plain (see sessionRow: nested colors invert into
// blocks under the reverse-video highlight).
func (m Model) sessDeleteRow(s golemclient.SessionSummary, width int, focused bool) string {
	bar := m.rowBar(s.ID, focused)
	status := m.rowStatus(s, focused)

	hintCols := max(width-sessBarCols-sessStatusCols, 1)

	if focused {
		return bar + status + ansi.Truncate(sessDeleteHint, hintCols, "…")
	}

	return bar + status + m.kit.Marker(kit.Error, ansi.Truncate(sessDeleteHint, hintCols, "…"))
}

// Session-row geometry: the current-session bar (one cell plus a space),
// the status column (the spinner glyph or its blank), the title gap
// before the right-aligned age, and the row indent under the cursor.
const (
	sessBarCols    = 2 // "▎ "
	sessStatusCols = 2 // glyph + space
	sessAgeGap     = 2 // spaces between title and age
)

// pickerVisibleRows caps how many list lines (rows + group headers) the
// picker renders — opencode's dialog maxHeight. Beyond it the list
// scrolls inside a fixed window; the modal never outgrows the screen
// and the query line plus key hints stay pinned at the bottom.
const pickerVisibleRows = 15

// sessRowWidth is the cell budget one picker row may span: the modal
// body's inner width (viewport minus the frame) minus the shared
// two-cell gutter (one on each flank, every row alike). The floor keeps
// a narrow terminal readable.
func (m Model) sessRowWidth() int {
	return max(m.vp.Width-modalChromeTotal-sessGutterCols, 20)
}

// modalChromeTotal mirrors kit's modal frame cost (two border columns
// plus horizontal padding on both flanks) for width budgeting here.
const modalChromeTotal = 6

// sessGutterCols is the gutter every row spends: one cell per flank
// (kit.RowGutter), cursor row and plain rows alike.
const sessGutterCols = 2 * kit.RowGutter

// sessionRow renders one picker row as aligned visual columns —
//
//	▎ ⠋ Fix auth middleware    2h ago
//
// the bar marks the session this client is in, the spinner (animated by
// spinTickMsg) marks a live turn, and the dim age stays right. No text
// markers: state reads from color and glyphs, not words. The raw
// session id never shows.
//
// focused rows render plain on purpose: the focus highlight is reverse
// video (kit.Row), and any colored span nested inside it has its
// foreground inverted into a background — the bar would become a bright
// block, the spinner and age colored rectangles. Glyphs alone carry the
// state while focused; color returns when the cursor moves off.
func (m Model) sessionRow(s golemclient.SessionSummary, now time.Time, width int, focused bool) string {
	name := s.Title

	if name == "" {
		name = dirName(s.Cwd)

		if name == "" {
			name = s.ID
		}
	}

	bar := m.rowBar(s.ID, focused)
	status := m.rowStatus(s, focused)

	age := ""
	if rel, ok := relTime(now, s.LastActive); ok {
		age = rel
	}

	// The title absorbs truncation: fixed columns (bar, status, age)
	// never lose cells to a long title.
	titleCols := max(width-sessBarCols-sessStatusCols-len(age)-sessAgeGap, 1)
	name = ansi.Truncate(name, titleCols, "…")

	if focused {
		return bar + status + name + strings.Repeat(" ", sessAgeGap) + age
	}

	return bar + status + name + strings.Repeat(" ", sessAgeGap) + m.kit.DimLine(age)
}

// sessionRunning reports whether a turn is live in the listed session.
// The server flag is a list-time snapshot; for the session this client is
// in, the local turn state is fresher and overrides it.
func (m Model) sessionRunning(s golemclient.SessionSummary) bool {
	if s.ID == m.sessionID {
		return m.turnActive
	}

	return s.Busy
}

// dirName is the final path segment of a working directory.
func dirName(cwd string) string {
	if cwd == "" {
		return ""
	}

	return filepath.Base(cwd)
}

// relTime renders a timestamp as a short relative age ("2h ago"). It
// reports false when the timestamp is missing or unparsable.
func relTime(now time.Time, rfc3339 string) (string, bool) {
	ts, err := time.Parse(time.RFC3339, rfc3339)
	if err != nil {
		return "", false
	}

	diff := now.Sub(ts)

	switch {
	case diff < time.Minute:
		return "just now", true
	case diff < time.Hour:
		return fmt.Sprintf("%dm ago", int(diff.Minutes())), true
	case diff < 24*time.Hour:
		return fmt.Sprintf("%dh ago", int(diff.Hours())), true
	case diff < 7*24*time.Hour:
		return fmt.Sprintf("%dd ago", int(diff.Hours()/24)), true
	default:
		return ts.Format("Jan 2"), true
	}
}
