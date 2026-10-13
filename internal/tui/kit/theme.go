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

// DefaultTheme is the built-in look, carried over from the original
// transcript styling.
func DefaultTheme() *Theme {
	return &Theme{
		Name: "default",
		Colors: map[Token]lipgloss.AdaptiveColor{
			Fg:        {Light: "0", Dark: "15"},
			Bg:        {},
			Accent:    {Light: "5", Dark: "14"},
			Dim:       {Light: "8", Dark: "8"},
			Error:     {Light: "9", Dark: "9"},
			OK:        {Light: "10", Dark: "10"},
			Warn:      {Light: "11", Dark: "11"},
			User:      {Light: "12", Dark: "12"},
			Assistant: {Light: "15", Dark: "15"},
			Tool:      {Light: "8", Dark: "8"},
			Border:    {Light: "8", Dark: "8"},
			Title:     {Light: "13", Dark: "13"},
			Selection: {Light: "6", Dark: "6"},
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
