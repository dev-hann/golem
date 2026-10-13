package kit_test

import (
	"strings"
	"testing"

	"github.com/dev-hann/golem/internal/tui/kit"

	tea "github.com/charmbracelet/bubbletea"
)

func pickKey(s string) tea.KeyMsg {
	switch s {
	case "up":
		return tea.KeyMsg{Type: tea.KeyUp}
	case "down":
		return tea.KeyMsg{Type: tea.KeyDown}
	case "j":
		return tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'j'}}
	case "k":
		return tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'k'}}
	case "enter":
		return tea.KeyMsg{Type: tea.KeyEnter}
	case "q":
		return tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'q'}}
	case "esc":
		return tea.KeyMsg{Type: tea.KeyEsc}
	default:
		panic("unknown key " + s)
	}
}

func TestKitPicker_ArrowKeysMoveAndEnterSelects(t *testing.T) {
	t.Parallel()

	p := kit.NewPicker(kit.New(kit.DefaultTheme()), "models", []string{"a", "b", "c"}, 0)

	p.Update(pickKey("down"))
	p.Update(pickKey("down"))
	p.Update(pickKey("up"))

	if p.Cursor != 1 {
		t.Fatalf("Cursor = %d, want 1", p.Cursor)
	}

	final, _ := p.Update(pickKey("enter"))
	picker := final.(*kit.Picker)

	item, ok := picker.Selection()
	if !ok || item != "b" {
		t.Errorf("Selection() = %q, %v; want b, true", item, ok)
	}
}

func TestKitPicker_JKAndClamping(t *testing.T) {
	t.Parallel()

	p := kit.NewPicker(kit.New(kit.DefaultTheme()), "models", []string{"a", "b"}, 0)

	p.Update(pickKey("k")) // above the top: stays
	if p.Cursor != 0 {
		t.Errorf("Cursor = %d after up-clamp, want 0", p.Cursor)
	}

	p.Update(pickKey("j"))
	p.Update(pickKey("j")) // below the bottom: stays
	if p.Cursor != 1 {
		t.Errorf("Cursor = %d after down-clamp, want 1", p.Cursor)
	}
}

func TestKitPicker_CancelYieldsNoSelection(t *testing.T) {
	t.Parallel()

	p := kit.NewPicker(kit.New(kit.DefaultTheme()), "models", []string{"a", "b"}, 0)

	final, _ := p.Update(pickKey("q"))
	picker := final.(*kit.Picker)

	if item, ok := picker.Selection(); ok {
		t.Errorf("Selection() = %q, %v; want none after cancel", item, ok)
	}
}

func TestKitPicker_ViewMarksCursor(t *testing.T) {
	t.Parallel()

	p := kit.NewPicker(kit.New(kit.DefaultTheme()), "pick", []string{"a", "b"}, 1)

	view := p.View()
	if !strings.Contains(view, "pick") || !strings.Contains(view, "▶ b") {
		t.Errorf("view missing title or cursor:\n%s", view)
	}
	if strings.Contains(view, "▶ a") {
		t.Errorf("wrong cursor marked:\n%s", view)
	}
}
