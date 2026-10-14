package kit_test

import (
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"

	"github.com/dev-hann/golem/internal/tui/kit"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/muesli/termenv"
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

	// TrueColor pins reverse video into the output so the cursor row is
	// assertable (Ascii strips every escape).
	p := kit.NewPicker(
		kit.NewWithRenderer(kit.DefaultTheme(), forcedRenderer(termenv.TrueColor)),
		"pick", []string{"a", "b"}, 1,
	)

	view := p.View()
	if !strings.Contains(view, "pick") {
		t.Errorf("view missing title:\n%s", view)
	}

	for _, line := range strings.Split(view, "\n") {
		if !strings.Contains(line, "b") {
			continue
		}

		if !strings.Contains(line, "\x1b[7") {
			t.Errorf("cursor row must carry reverse video:\n%q", line)
		}

		if !strings.Contains(line, " b ") {
			t.Errorf("cursor row keeps its gutter padding around the item:\n%q", line)
		}

		break
	}

	for _, line := range strings.Split(view, "\n") {
		if strings.Contains(line, "a") && strings.Contains(line, "\x1b[7") {
			t.Errorf("wrong cursor marked:\n%s", view)
		}
	}

	if strings.Contains(view, "▶") {
		t.Errorf("picker rows must not carry a cursor arrow:\n%s", view)
	}
}

func TestKitPicker_ViewRespectsWidth(t *testing.T) {
	t.Parallel()

	k := kit.New(kit.DefaultTheme())

	tests := []struct {
		name  string
		title string
		items []string
		width int
	}{
		{
			name:  "long title truncates",
			title: "models for zai-coding (30) — ↑↓ move, enter select, q to type an id",
			items: []string{"glm-5.3", "glm-5.3-air"},
			width: 40,
		},
		{
			name:  "long item truncates",
			title: "models",
			items: []string{"openrouter/" + strings.Repeat("very-long-model-name/", 8)},
			width: 40,
		},
		{
			name:  "wide glyphs count double",
			title: "모델 선택",
			items: []string{strings.Repeat("한", 30)},
			width: 20,
		},
		{
			name:  "hint line truncates",
			title: "models",
			items: []string{"a"},
			width: 24, // the hint alone is 38 cells
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			p := kit.NewPicker(k, tt.title, tt.items, 0)
			p.SetWidth(tt.width)

			for i, line := range strings.Split(p.View(), "\n") {
				if w := ansi.StringWidth(line); w > tt.width {
					t.Errorf("row %d width %d exceeds %d:\n%s", i, w, tt.width, p.View())
				}
			}
		})
	}
}

func TestKitPicker_ZeroWidthRendersUnbounded(t *testing.T) {
	t.Parallel()

	p := kit.NewPicker(kit.New(kit.DefaultTheme()), "pick", []string{"a"}, 0)
	p.SetWidth(0)

	if !strings.Contains(p.View(), "a") {
		t.Errorf("zero width must not drop items:\n%s", p.View())
	}
}
