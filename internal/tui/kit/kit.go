package kit

import (
	"strings"

	"github.com/charmbracelet/lipgloss"
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

// User renders the user-message prefix ("you> " style bold, user color).
func (k *Kit) User(prefix, text string) string {
	return k.styles[User].Bold(true).Render(prefix) + text
}

// Assistant renders the assistant-message prefix.
func (k *Kit) Assistant(prefix, text string) string {
	return k.styles[Assistant].Render(prefix) + text
}

// ToolLine renders a muted tool-activity line.
func (k *Kit) ToolLine(text string) string {
	return k.styles[Tool].Render(text)
}

// ErrLine renders an error message.
func (k *Kit) ErrLine(text string) string {
	return k.styles[Error].Render("error: ") + k.styles[Error].Render(text)
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

// StatusBar renders a one-line status bar: dim join of parts with the
// first part emphasized.
func (k *Kit) StatusBar(parts ...string) string {
	if len(parts) == 0 {
		return ""
	}

	out := k.styles[Accent].Render(parts[0])
	for _, part := range parts[1:] {
		out += k.styles[Dim].Render(" · ") + part
	}

	return out
}

// Prompt renders an input prompt label (the colored "API key:" style).
func (k *Kit) Prompt(text string) string {
	return k.styles[Accent].Bold(true).Render(text)
}

// Selection renders the picker cursor marker.
func (k *Kit) Selection(line string) string {
	return k.styles[Selection].Reverse(true).Render(" " + line + " ")
}
