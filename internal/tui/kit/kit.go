package kit

import (
	"strings"

	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"
)

// Kit derives cached styles from a theme and renders the shared
// components. All TUI output goes through here.
type Kit struct {
	theme  *Theme
	r      *lipgloss.Renderer
	styles map[Token]lipgloss.Style
}

// New builds a Kit on the default renderer.
func New(theme *Theme) *Kit {
	return NewWithRenderer(theme, lipgloss.DefaultRenderer())
}

// NewWithRenderer builds a Kit against a pinned renderer (tests force
// color profiles; NO_COLOR terminals degrade automatically).
func NewWithRenderer(theme *Theme, r *lipgloss.Renderer) *Kit {
	k := &Kit{theme: theme, r: r, styles: map[Token]lipgloss.Style{}}

	for _, token := range Tokens {
		k.styles[token] = r.NewStyle().Foreground(theme.Colors[token])
	}

	return k
}

// Theme returns the active theme.
func (k *Kit) Theme() *Theme { return k.theme }

// Style exposes a token's base style for the rare component needing raw
// composition — still token-driven, never a raw color.
func (k *Kit) Style(token Token) lipgloss.Style { return k.styles[token] }

// --- transcript lines -------------------------------------------------

// ToolLine renders a muted tool-activity line.
func (k *Kit) ToolLine(text string) string {
	return k.styles[Tool].Render(text)
}

// errPrefix labels every error line.
const errPrefix = "error: "

// ErrLine renders an error message. A positive width wraps the labeled
// message to fit; width <= 0 leaves it unwrapped (callers printing to a
// terminal without knowing its width).
func (k *Kit) ErrLine(width int, text string) string {
	s := errPrefix + text

	if width > 0 {
		s = k.WrapPlain(width, s)
	}

	return k.styles[Error].Render(s)
}

// OKLine renders a success message.
func (k *Kit) OKLine(text string) string {
	return k.styles[OK].Render(text)
}

// WarnLine renders a warning message.
func (k *Kit) WarnLine(text string) string {
	return k.styles[Warn].Render("warning: ") + k.styles[Warn].Render(text)
}

// DimLine renders secondary/muted text.
func (k *Kit) DimLine(text string) string {
	return k.styles[Dim].Render(text)
}

// Marker renders a short status glyph (✓/✗/…) in a token's color.
func (k *Kit) Marker(token Token, glyph string) string {
	return k.styles[token].Render(glyph)
}

// SpinnerFrames is the braille spinner's frame sequence (⠋⠙⠹…) — one
// glyph per render tick while a turn runs.
var SpinnerFrames = []string{"⠋", "⠙", "⠹", "⠸", "⠼", "⠴", "⠦", "⠧", "⠇", "⠏"}

// Spinner renders the spinner's frame (any int; the sequence cycles) in
// the accent token's color — a live-turn marker that needs no words.
func (k *Kit) Spinner(frame int) string {
	n := len(SpinnerFrames)

	return k.Marker(Accent, SpinnerFrames[((frame%n)+n)%n])
}

// --- structure ---------------------------------------------------------

// Title renders a section title.
func (k *Kit) Title(text string) string {
	return k.styles[Title].Bold(true).Render(text)
}

// Divider renders a full-width horizontal rule (pass the width).
func (k *Kit) Divider(width int) string {
	if width <= 0 {
		width = defaultDividerWidth
	}

	return k.styles[Border].Render(strings.Repeat("─", width))
}

// defaultDividerWidth is used when a width is unknown.
const defaultDividerWidth = 40

// Prompt renders an input prompt label (the colored "API key:" style).
func (k *Kit) Prompt(text string) string {
	return k.styles[Accent].Bold(true).Render(text)
}

// Box renders content inside a rounded border frame (the composer frame).
// leftBorder colors the left rail — the composer's mode edge — while the
// rest of the frame stays in the border token.
func (k *Kit) Box(content string, leftBorder Token) string {
	return k.r.NewStyle().
		Border(lipgloss.RoundedBorder()).
		BorderForeground(k.theme.Colors[Border]).
		BorderLeftForeground(k.theme.Colors[leftBorder]).
		Render(content)
}

// WrapPlain hard-wraps text so no display row exceeds width cells,
// preserving ANSI escapes and accounting for wide glyphs (CJK). Rows
// already within width pass through untouched; width < 1 disables
// wrapping. Trailing spaces dropped at a wrap point never carried
// content, so wrapped rows are trimmed right to honor the cap.
func (k *Kit) WrapPlain(width int, text string) string {
	if width < 1 {
		return text
	}

	rows := make([]string, 0, strings.Count(text, "\n")+1)

	for _, line := range strings.Split(text, "\n") {
		if ansi.StringWidth(line) <= width {
			rows = append(rows, line)

			continue
		}

		for _, seg := range strings.Split(ansi.Wrap(line, width, ""), "\n") {
			rows = append(rows, strings.TrimRight(seg, " "))
		}
	}

	return strings.Join(rows, "\n")
}

// Bar geometry: the "▎" bar, its space, and a floor for narrow terminals.
const (
	barCols    = 2
	minBarText = 10
)

// MessageBar renders message text with a colored left bar spanning every
// wrapped line — the transcript's identity marker for chat turns. Body
// text stays in the default foreground for readability.
func (k *Kit) MessageBar(token Token, width int, text string) string {
	inner := max(width-barCols, minBarText)

	bar := k.styles[token].Render("▎")

	barred := make([]string, 0, 8)

	for _, seg := range strings.Split(k.WrapPlain(inner, text), "\n") {
		barred = append(barred, bar+" "+seg)
	}

	return strings.Join(barred, "\n")
}

// Sticky geometry: the "▎ " bar and its one space.
const stickyBarCols = 2

// StickyUser renders the one-line pinned reminder of the most recent user
// message — a user-colored bar plus the truncated first line of the text.
func (k *Kit) StickyUser(width int, text string) string {
	first := strings.SplitN(text, "\n", 2)[0]
	limit := max(width-stickyBarCols, 1)

	return k.styles[User].Render("▎ " + ansi.Truncate(first, limit, "…"))
}

// StatusBar renders the one-line status strip between the transcript and
// the composer: a token-colored segment padded to width, truncated with
// an ellipsis on overflow. No right-aligned hint — the strip carries the
// agent state alone; interrupt affordances live on keys and docs.
func (k *Kit) StatusBar(width int, left string, token Token) string {
	if width < 1 {
		return ""
	}

	return k.styles[token].Render(truncateTo(left, width))
}

// truncateTo cuts s to limit cells with an ellipsis, tolerating a zero
// limit where ansi.Truncate's tail would not fit.
func truncateTo(s string, limit int) string {
	if limit < 1 {
		return ""
	}

	return ansi.Truncate(s, limit, "\u2026")
}

// modalPaddingH is the modal's inner horizontal padding in cells.
const modalPaddingH = 2

// modalChrome is the frame a modal adds around its body: the two border
// columns plus the horizontal padding on both flanks.
const modalChrome = 2 + 2*modalPaddingH

// Modal renders the shared overlay panel: a title line over a padded,
// framed, background-filled body. Every overlay in the app (help,
// pickers) uses this shape so their look cannot drift apart.
//
// A positive width caps the panel at the terminal: body rows wrap at
// the inner width and the title truncates. width <= 0 sizes the panel
// to its content (callers without a terminal width).
func (k *Kit) Modal(width int, title, body string) string {
	if width > modalChrome {
		inner := width - modalChrome
		body = k.WrapPlain(inner, body)
		title = ansi.Truncate(title, inner, "…")
	} else {
		width = 0
	}

	content := k.Title(title) + "\n" + strings.TrimRight(body, "\n")

	panel := 0
	for _, line := range strings.Split(content, "\n") {
		if w := ansi.StringWidth(line); w > panel {
			panel = w
		}
	}

	style := k.r.NewStyle().
		Border(lipgloss.RoundedBorder()).
		BorderForeground(k.theme.Colors[Border]).
		Background(k.theme.Colors[Bg]).
		Padding(0, modalPaddingH).
		Width(panel + 2*modalPaddingH)

	if width > 0 {
		style = style.MaxWidth(width)
	}

	return style.Render(content)
}

// Overlay merges a modal over the base view, centered, leaving the base
// visible around it. ANSI-aware: styled base lines are cut at the modal
// bounds and reset codes keep color state from bleeding across the seam.
func (k *Kit) Overlay(width, height int, base, modal string) string {
	if width <= 0 || height <= 0 {
		// Pre-resize frame: stack the modal under the base rather than
		// dropping it.
		return base + "\n" + modal
	}

	baseLines := strings.Split(base, "\n")
	modalLines := strings.Split(strings.TrimRight(modal, "\n"), "\n")

	modalWidth := 0
	for _, line := range modalLines {
		if w := ansi.StringWidth(line); w > modalWidth {
			modalWidth = w
		}
	}

	x0 := max((width-modalWidth)/2, 0)
	y0 := max((height-len(modalLines))/2, 0)

	lines := make([]string, 0, height)
	for y := range height {
		baseLine := ""
		if y < len(baseLines) {
			baseLine = baseLines[y]
		}

		if y < y0 || y >= y0+len(modalLines) {
			lines = append(lines, baseLine)

			continue
		}

		modalLine := ansi.Truncate(modalLines[y-y0], width-x0, "")

		prefix := ansi.Truncate(baseLine, x0, "")
		suffix := ansi.TruncateLeft(baseLine, x0+ansi.StringWidth(modalLine), "")

		lines = append(lines, prefix+"\x1b[0m"+modalLine+"\x1b[0m"+suffix)
	}

	return strings.Join(lines, "\n")
}

// Fade renders escape-free plain text in the theme's dim color — the
// monochrome backdrop tone behind an open modal.
func (k *Kit) Fade(plain string) string {
	return k.styles[Dim].Render(plain)
}

// RowGutter is the flank padding every list row spends — cursor rows
// render it through Selection, the rest as plain spaces — so all rows
// share one text origin and moving the cursor shifts nothing.
const RowGutter = 1

// Row renders one list row in the shared gutter; selected highlights it
// with reverse video (no arrow prefix: a marker would offset the row's
// text against its neighbors every time the cursor moves).
func (k *Kit) Row(text string, selected bool) string {
	if selected {
		return k.styles[Selection].Reverse(true).
			Render(" " + text + " ")
	}

	return " " + text + " "
}
