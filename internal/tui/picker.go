// Package-level list picker: a minimal Bubble Tea selector reused by the
// setup wizard (and later the TUI) for choosing from short lists.

package tui

import (
	"fmt"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
)

// PickerKey carries the chosen item back to the caller.
type PickerKey struct{ Index int }

// pickerCancel signals dismissal without a choice.
type pickerCancel struct{}

// Picker is a list selector model: arrow keys or j/k move, enter picks,
// q/esc cancels.
type Picker struct {
	Title  string
	Items  []string
	Cursor int

	chosen int
	done   bool
	cancel bool
}

// NewPicker builds a picker with the cursor prepositioned.
func NewPicker(title string, items []string, cursor int) *Picker {
	if cursor < 0 {
		cursor = 0
	}

	if cursor >= len(items) {
		cursor = len(items) - 1
	}

	return &Picker{Title: title, Items: items, Cursor: cursor, chosen: -1}
}

// Init implements tea.Model.
func (p *Picker) Init() tea.Cmd { return nil }

// Update implements tea.Model.
func (p *Picker) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.KeyMsg:
		switch msg.String() { //nolint:exhaustive // key names, not enum values
		case "up", "k":
			p.move(-1)
		case "down", "j":
			p.move(1)
		case "enter":
			p.chosen = p.Cursor
			p.done = true

			return p, tea.Quit
		case "q", "esc", "ctrl+c":
			p.cancel = true
			p.done = true

			return p, tea.Quit
		}
	case pickerCancel:
		p.cancel = true
		p.done = true

		return p, tea.Quit
	}

	return p, nil
}

// View implements tea.Model.
func (p *Picker) View() string {
	var body strings.Builder

	body.WriteString(p.Title)
	body.WriteString("\n")

	for i, item := range p.Items {
		cursor := " "
		if i == p.Cursor {
			cursor = "▶"
		}

		fmt.Fprintf(&body, "%s %s\n", cursor, item) //nolint:errcheck // strings.Builder
	}

	body.WriteString("\n↑↓ move · enter select · q cancel")

	return body.String()
}

// move shifts the cursor by delta, clamping.
func (p *Picker) move(delta int) {
	p.Cursor += delta
	if p.Cursor < 0 {
		p.Cursor = 0
	}

	if p.Cursor >= len(p.Items) {
		p.Cursor = len(p.Items) - 1
	}
}

// Selection returns the chosen item and whether one was made.
func (p *Picker) Selection() (string, bool) {
	if p.cancel || p.chosen < 0 || p.chosen >= len(p.Items) {
		return "", false
	}

	return p.Items[p.chosen], true
}

// PickFromList runs the picker full-screen; ok is false when the user
// cancelled or no terminal is available (callers fall back to line input).
func PickFromList(title string, items []string, cursor int) (item string, ok bool) {
	program := tea.NewProgram(NewPicker(title, items, cursor), tea.WithAltScreen())

	final, err := program.Run()
	if err != nil {
		return "", false
	}

	picker, alright := final.(*Picker)
	if !alright {
		return "", false
	}

	return picker.Selection()
}
