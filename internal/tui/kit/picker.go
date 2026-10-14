// Themed list picker: arrow keys or j/k move, enter picks, q/esc cancels.

package kit

import (
	"strings"

	tea "github.com/charmbracelet/bubbletea"
)

// Picker is a list selector model rendered through the kit theme.
type Picker struct {
	Title  string
	Items  []string
	Cursor int

	kit    *Kit
	chosen int
	cancel bool
	width  int
}

// NewPicker builds a picker with the cursor prepositioned.
func NewPicker(k *Kit, title string, items []string, cursor int) *Picker {
	if cursor < 0 {
		cursor = 0
	}

	if cursor >= len(items) {
		cursor = len(items) - 1
	}

	return &Picker{Title: title, Items: items, Cursor: cursor, kit: k, chosen: -1}
}

// Init implements tea.Model.
func (p *Picker) Init() tea.Cmd { return nil }

// Update implements tea.Model.
func (p *Picker) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	if key, isKey := msg.(tea.KeyMsg); isKey {
		switch key.String() { //nolint:exhaustive // key names, not enum values
		case "up", "k":
			p.move(-1)
		case "down", "j":
			p.move(1)
		case "enter":
			p.chosen = p.Cursor
			p.done()

			return p, tea.Quit
		case "q", "esc", "ctrl+c":
			p.cancel = true
			p.done()

			return p, tea.Quit
		}
	}

	return p, nil
}

// done marks the picker finished (kept as a seam for future animations).
func (p *Picker) done() {}

// SetWidth caps every rendered row at width cells: titles, items, and
// the hint line truncate so a narrow terminal never wraps the panel.
// Zero or negative disables the cap (unbounded, content-sized).
func (p *Picker) SetWidth(width int) {
	p.width = width
}

// row renders one capped row in the shared gutter: truncation counts
// the gutter cells so every row spans the same width whether or not
// the cursor highlights it.
func (p *Picker) row(text string, selected bool) string {
	if inner := p.width - 2*RowGutter; inner > 0 {
		text = truncateTo(text, inner)
	}

	return p.kit.Row(text, selected)
}

// View implements tea.Model.
func (p *Picker) View() string {
	var body strings.Builder

	title := p.Title
	if p.width > 0 {
		title = truncateTo(title, p.width)
	}

	body.WriteString(p.kit.Title(title))
	body.WriteString("\n")

	for i, item := range p.Items {
		body.WriteString(p.row(item, i == p.Cursor))
		body.WriteString("\n")
	}

	body.WriteString("\n")

	hint := "↑↓ move · enter select · q cancel"
	if p.width > 0 {
		hint = truncateTo(hint, p.width)
	}

	body.WriteString(p.kit.DimLine(hint))

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
func (k *Kit) PickFromList(title string, items []string, cursor int) (item string, ok bool) {
	program := tea.NewProgram(NewPicker(k, title, items, cursor), tea.WithAltScreen())

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
