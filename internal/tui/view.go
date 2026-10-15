// Rendering: the layout stack (header, transcript viewport, status
// strip, composer), the sticky question line, and the status-line state
// labels.

package tui

import (
	"fmt"
	"strings"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/dev-hann/golem/internal/tui/kit"
	"github.com/dev-hann/golem/pkg/golemclient"
)

// View implements tea.Model.
func (m Model) View() tea.View {
	var content string

	switch {
	case m.connErr != "":
		content = "golem: " + m.connErr + "\n"
	case m.mode == modeCommand:
		content = m.modalized(m.kit.Modal(m.vp.Width(), "commands", m.commandBody()))
	case m.mode == modeModels:
		content = m.modalized(m.kit.Modal(m.vp.Width(), "models · "+m.provider, m.modelsBody()))
	case m.mode == modeSessions:
		content = m.modalized(m.kit.Modal(m.vp.Width(), "sessions", m.sessionsBody()))
	default:
		// Chat (and future modes) fall through to the full layout.
		content = m.layout()
	}

	v := tea.NewView(content)
	v.AltScreen = true
	v.MouseMode = tea.MouseModeCellMotion
	v.WindowTitle = "golem"

	// OSC 7501: report the agent's coarse state to the terminal (the tab
	// dot in supporting multiplexers). Map the status strip's states onto
	// the protocol's working / waiting / idle vocabulary.
	switch {
	case m.connErr != "":
		v.ProgramStatus = &tea.ProgramStatus{State: tea.ProgramStateError}
	case m.turnActive:
		v.ProgramStatus = &tea.ProgramStatus{State: tea.ProgramStateWorking}
	case m.mode == modeSessions || m.mode == modeModels || m.mode == modeCommand:
		v.ProgramStatus = &tea.ProgramStatus{State: tea.ProgramStateBlocked}
	default:
		v.ProgramStatus = &tea.ProgramStatus{State: tea.ProgramStateIdle}
	}

	return v
}

// layout composes the full chat screen through the active kit.
func (m Model) layout() string {
	var b strings.Builder
	if m.headerShown() {
		b.WriteString(m.headerLine() + "\n")
	}

	if text := m.stickyText(); text != "" {
		b.WriteString(m.kit.StickyUser(m.vp.Width(), text) + "\n")
		b.WriteString(m.shrunkViewport() + "\n")
	} else if m.showWelcome() {
		// Fresh launch: the centered banner owns the transcript region
		// (ADR-0018's pre-session state) until the first message lands.
		b.WriteString(m.welcomeView() + "\n")
	} else {
		b.WriteString(m.vp.View() + "\n")
	}

	b.WriteString(m.statusLine() + "\n")

	edge := kit.Border
	if m.leaderArmed {
		edge = kit.Accent
	}

	b.WriteString("\n") // the gap between transcript and composer
	b.WriteString(m.kit.Box(m.input.View(), edge))

	return padScreen(b.String())
}

// padScreen frames the composed screen with its padding: every row gets
// the left pad, blank rows sit above and below. The caller already
// shrunk the viewport by layoutChromeRows so the padded result still
// fills the terminal height exactly.
func padScreen(body string) string {
	pad := strings.Repeat(" ", screenPadH)

	rows := strings.Split(body, "\n")
	for i := range rows {
		rows[i] = pad + rows[i]
	}

	blank := make([]string, screenPadV)
	for i := range blank {
		blank[i] = pad
	}

	out := make([]string, 0, len(rows)+2*screenPadV)
	out = append(out, blank...)
	out = append(out, rows...)
	out = append(out, blank...)

	return strings.Join(out, "\n")
}

// headerShown reports whether the session header renders on this
// terminal — roomy enough to afford the row, else the transcript keeps
// it. The screenPadV offset shifts by the same amount, so the padding
// geometry stays in sync with the resize chrome arithmetic.
func (m Model) headerShown() bool {
	return m.height >= headerMinHeight
}

// headerRowsIfShown is the header's chrome contribution at resize time.
func headerRowsIfShown(height int) int {
	if height >= headerMinHeight {
		return headerRows
	}

	return 0
}

// headerLine renders the one-row session header: the title (or a dim
// placeholder naming the state) with the working-directory tail —
// always exactly one row, truncated to the viewport width.
func (m Model) headerLine() string {
	title, token := m.headerTitle()

	left := title
	if dir := m.headerDir(); dir != "" {
		left += headerDirSep + dir
	}

	return m.kit.StatusBar(m.vp.Width(), left, token)
}

// headerTitle resolves the header's title text and token: a titled
// session names itself, an untitled one and the pre-session state fall
// back to dim placeholders.
func (m Model) headerTitle() (string, kit.Token) {
	switch {
	case m.sessionID == "":
		return "no session yet", kit.Dim
	case m.sessionTitle != "":
		return m.sessionTitle, kit.Title
	default:
		return "untitled session", kit.Dim
	}
}

// headerDir renders the working-directory tail beside the title: the
// final path segment (empty until a session exists).
func (m Model) headerDir() string {
	return dirName(m.sessionCwd)
}

// headerDirSep joins the header's title and directory segments.
const headerDirSep = " · "

// statusLine renders the agent-state strip: what the turn is doing (or
// the model and last usage while idle). The interrupt hint lives on the
// key itself, not in the strip.
func (m Model) statusLine() string {
	left, token := m.activeState()
	return m.kit.StatusBar(m.vp.Width(), left, token)
}

// activeState resolves the status strip's label and token color from the
// turn state. States, in priority order: a live retry outranks
// everything (its stall is the thing needing explanation), then the
// in-flight tool, then synthesizing (results back at the model), then
// reasoning, responding, thinking. Reasoned-then-text shows responding —
// the answer has begun.
func (m Model) activeState() (string, kit.Token) {
	if m.turnActive {
		return m.activeTurnState()
	}

	left, token := "● idle", kit.Dim

	switch {
	case m.sessionID == "" && (len(m.pendingMsgs) > 0 || m.pendingModel != ""):
		left = "● creating session"
		token = kit.Accent
	case m.modelName != "":
		left = "● " + m.modelName + usageSummary(m.lastUsage) + sessionUsageSuffix(m.usageIn, m.usageOut)
	}

	return left, token
}

// activeTurnState resolves the strip while a turn runs: retry, tool,
// synthesizing, responding, reasoning, thinking — in that priority. The
// leading glyph is the animated spinner (the strip's motion marker);
// only the idle strip is static.
func (m Model) activeTurnState() (string, kit.Token) {
	spin := m.kit.Spinner(m.spinFrame) + " "

	switch {
	case m.retryState != nil:
		return spin + fmt.Sprintf("retrying %d/%d · %s",
			m.retryState.Attempt, m.retryState.Retries, shortDur(m.retryState.DelayMs)), kit.Warn
	case m.activeTool != "":
		return spin + m.activeTool + " — " + argSummary(m.activeTool, m.activeArgs), kit.Tool
	case m.synthDone != nil:
		return spin + synthMark(m.synthDone) + " · working", kit.Accent
	case m.streamedText:
		return spin + "responding", kit.Accent
	case m.reasoned:
		return spin + "reasoning", kit.Accent
	default:
		return spin + "thinking", kit.Accent
	}
}

// retryInfo snapshots one announced provider retry.
type retryInfo struct {
	Attempt, Retries, DelayMs int
}

// toolDone remembers the last ToolCallEnd while the turn waits for the
// model's follow-up — the synthesizing state's summary line.
type toolDone struct {
	Tool string
	OK   bool
}

// synthMark renders the synthesizing summary: the finished tool's outcome
// glyph and name.
func synthMark(d *toolDone) string {
	mark := "✓"
	if !d.OK {
		mark = "✗"
	}

	return mark + " " + d.Tool
}

// shortDur renders a backoff milliseconds count compactly (4s, 1.5s).
func shortDur(ms int) string {
	if ms%1000 == 0 {
		return fmt.Sprintf("%ds", ms/1000)
	}

	return fmt.Sprintf("%.1fs", float64(ms)/1000)
}

// usageSummary renders the last turn's token counts, or "" before the
// first turn reports usage.
func usageSummary(u golemclient.Usage) string {
	if u.InputTokens == 0 && u.OutputTokens == 0 {
		return ""
	}

	return fmt.Sprintf(" · ↑%s ↓%s", tokenCount(u.InputTokens), tokenCount(u.OutputTokens))
}

// sessionUsageSuffix renders the session-cumulative token total, or ""
// while the session has no recorded usage yet (restored from
// session_meta.usage, accumulated from turn_end events).
func sessionUsageSuffix(in, out int64) string {
	if in == 0 && out == 0 {
		return ""
	}

	return fmt.Sprintf(" · Σ%s", tokenCount(in+out))
}

// tokenCount shortens a token count (1.2k, 15k, 2.1M).
func tokenCount(n int64) string {
	switch {
	case n >= 1_000_000:
		return fmt.Sprintf("%.1fM", float64(n)/1_000_000)
	case n >= 1_000:
		return fmt.Sprintf("%.1fk", float64(n)/1_000)
	default:
		return fmt.Sprintf("%d", n)
	}
}

// stickyText returns the latest user message pushed above the rendered
// window's top row — the question whose bar sits directly above what
// the transcript shows — or "" when every question's bar is inside the
// window (or none exists). A newly submitted question does not take
// the pin while its bar is on screen: the pin cedes only when streamed
// content pushes the newer bar past the top (ADR-0028, push-out).
func (m Model) stickyText() string {
	top, _ := m.renderedWindow()

	latest := ""

	for _, span := range m.userSpans {
		if span.start >= top {
			break // spans ascend: nothing later is pushed out either
		}

		latest = span.text
	}

	return latest
}

// shrunkViewport renders the transcript window under a pinned sticky
// line, one row shorter so the total screen height stays constant.
func (m Model) shrunkViewport() string {
	offset, height := m.renderedWindow()

	vp := m.vp
	// Height first: SetYOffset clamps to the (new, shorter) window's
	// max — setting the offset first would clamp it to the old window's
	// max and lose the final row.
	vp.SetHeight(height)
	vp.SetYOffset(offset)

	return vp.View()
}

// renderedWindow returns the content-line window [offset, offset+height)
// the transcript actually renders: the sticky bar shortens the viewport
// by one row, and a window pinned to the tail must advance by that row —
// pinning against the full height alone strands the last content line
// below the fold, hidden behind the composer box. A scrolled-back window
// stays top-aligned: it only loses its final row.
func (m Model) renderedWindow() (offset, height int) {
	height = max(m.vp.Height()-stickyRows, viewportMinHeight)
	offset = m.vp.YOffset()

	if m.vp.AtBottom() {
		offset = max(m.totalLines()-height, 0)
	}

	return offset, height
}

// totalLines counts the rendered transcript's content lines.
func (m Model) totalLines() int {
	return len(strings.Split(m.content, "\n"))
}

// stickyRows is the row count the pinned sticky header borrows from the
// viewport.
const stickyRows = 1

// modalized renders the modal floating over a faded, escape-free backdrop
// of the full layout — the base stays visible in the theme's dim tone.
func (m Model) modalized(modal string) string {
	backdrop := m.kit.Fade(ansi.Strip(m.layout()))

	return m.kit.Overlay(m.width, m.height, backdrop, modal)
}
