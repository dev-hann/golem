// Package kit is golem's TUI design system: a themeable token palette and
// the shared components every surface renders through. No raw colors
// outside this package; a theme is pure data so future config loading and
// runtime switching are drop-ins.
package kit

import (
	"fmt"
	"strconv"
	"strings"

	"github.com/charmbracelet/lipgloss"
)

// Token is a semantic color slot. Components reference tokens only —
// retheming means changing what a token maps to, never component code.
type Token string

// The complete token set.
const (
	Fg        Token = "fg"
	Bg        Token = "bg"
	Accent    Token = "accent"
	Dim       Token = "dim"
	Error     Token = "error"
	OK        Token = "ok"
	Warn      Token = "warn"
	User      Token = "user"
	Assistant Token = "assistant"
	Tool      Token = "tool"
	Border    Token = "border"
	Title     Token = "title"
	Selection Token = "selection"
)

// Tokens lists every token; themes must cover them all.
var Tokens = []Token{
	Fg, Bg, Accent, Dim, Error, OK, Warn, User, Assistant, Tool, Border, Title, Selection,
}

// Theme is a pure-data palette: token → color (adaptive for light/dark
// terminals). Serializable by construction for future config loading.
type Theme struct {
	Name   string
	Colors map[Token]lipgloss.AdaptiveColor
}

// DefaultTheme is the built-in look: Tokyo Night (folke/tokyonight), the
// Night variant for dark terminals and Day for light ones.
func DefaultTheme() *Theme {
	return &Theme{
		Name: "tokyo-night",
		Colors: map[Token]lipgloss.AdaptiveColor{
			Fg:        {Light: "#3760bf", Dark: "#c0caf5"},
			Bg:        {Light: "#e1e2e7", Dark: "#1a1b26"},
			Accent:    {Light: "#2e7de9", Dark: "#7aa2f7"},
			Dim:       {Light: "#a1a6c5", Dark: "#565f89"},
			Error:     {Light: "#f52a65", Dark: "#f7768e"},
			OK:        {Light: "#587539", Dark: "#9ece6a"},
			Warn:      {Light: "#8c6c3e", Dark: "#e0af68"},
			User:      {Light: "#007197", Dark: "#7dcfff"},
			Assistant: {Light: "#3760bf", Dark: "#c0caf5"},
			Tool:      {Light: "#848cb5", Dark: "#737aa2"},
			Border:    {Light: "#a1a6c5", Dark: "#3b4261"},
			Title:     {Light: "#9854f1", Dark: "#bb9af7"},
			Selection: {Light: "#2e7de9", Dark: "#7aa2f7"},
		},
	}
}

// isHexDigitRune reports whether r is 0-9, a-f, or A-F.
func isHexDigitRune(r rune) bool {
	return (r >= '0' && r <= '9') || (r >= 'a' && r <= 'f') || (r >= 'A' && r <= 'F')
}

// colorTerm reports whether s is a valid theme color: a hex value
// (#rgb/#rgba/#rrggbb/#rrggbbaa) or an ANSI 256 id (digits).
func colorTerm(s string) bool {
	if s == "" {
		return false
	}

	if strings.HasPrefix(s, "#") {
		hex := strings.TrimPrefix(s, "#")

		validLen := len(hex) == 3 || len(hex) == 4 || len(hex) == 6 || len(hex) == 8
		if !validLen {
			return false
		}

		for _, r := range hex {
			if !isHexDigitRune(r) {
				return false
			}
		}

		return true
	}

	for _, r := range s {
		_, err := strconv.Atoi(string(r))
		if err != nil {
			return false
		}
	}

	return true
}

// ThemeFromMap builds a theme from plain color strings (hex or ANSI
// numbers), inheriting unspecified tokens from the default theme. This is
// the exact shape a future [theme] config section will feed in.
func ThemeFromMap(name string, colors map[Token]string) (*Theme, error) {
	theme := DefaultTheme()
	theme.Name = name

	for token, value := range colors {
		if !colorTerm(value) {
			return nil, fmt.Errorf("kit: theme %q: invalid color %q for token %q", name, value, token)
		}

		theme.Colors[token] = lipgloss.AdaptiveColor{Light: value, Dark: value}
	}

	return theme, nil
}
