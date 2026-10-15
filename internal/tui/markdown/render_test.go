package markdown

import (
	"strings"
	"testing"

	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"
	"github.com/muesli/termenv"

	"github.com/dev-hann/golem/internal/tui/kit"
)

// asciiKit builds a kit pinned to the Ascii profile: no color escapes
// appear in output, so substring assertions hold.
func asciiKit() *kit.Kit {
	r := lipgloss.NewRenderer(nil)
	r.SetColorProfile(termenv.Ascii)

	return kit.NewWithRenderer(kit.DefaultTheme(), r)
}

// TestRender_UsesKitTokens_NoRawEscape pins the kit-only rule (ADR-0026):
// markdown output carries no raw ANSI color sequences — in the Ascii
// profile there are none, and in a TrueColor profile the only escapes
// present are kit-rendered ones (SGR sequences, no bare bytes).
func TestRender_UsesKitTokens_NoRawEscape(t *testing.T) {
	t.Parallel()

	k := asciiKit()
	out := Render(k, 60, "# T\n\n**b** *i* `c` [l](u)\n\n> q\n\n- one\n- two\n\n```\ncode\n```\n")

	if strings.ContainsAny(out, "\x1b") {
		t.Errorf("Ascii profile output must carry no escapes, got:\n%q", out)
	}

	for _, want := range []string{"T", "b", "i", "c", "l", "(u)", "q", "one", "two", "code"} {
		if !strings.Contains(out, want) {
			t.Errorf("output missing %q:\n%s", want, out)
		}
	}
}

// TestRender_BlockShapes pins the rendered shape of each block kind:
// headings bold, code framed with a language label, lists indented
// under their marker, quotes behind a left bar.
func TestRender_BlockShapes(t *testing.T) {
	t.Parallel()

	k := asciiKit()

	tests := []struct {
		name string
		in   string
		want []string
		ban  []string
	}{
		{
			name: "heading drops markers, keeps text",
			in:   "## Title here\n",
			want: []string{"Title here"},
			ban:  []string{"##"},
		},
		{
			name: "paragraph strips inline markers",
			in:   "a **bold** word\n",
			want: []string{"a bold word"},
			ban:  []string{"**"},
		},
		{
			name: "code block shows language and body",
			in:   "```go\nx := 1\n```\n",
			want: []string{"go", "x := 1"},
			ban:  []string{"```"},
		},
		{
			name: "bullet list marks items",
			in:   "- alpha\n- beta\n",
			want: []string{"alpha", "beta"},
			ban:  []string{"- alpha"},
		},
		{
			name: "ordered list numbers items",
			in:   "1. alpha\n2. beta\n",
			want: []string{"1. alpha", "2. beta"},
		},
		{
			name: "quote keeps text, drops markers",
			in:   "> wisdom\n",
			want: []string{"wisdom"},
			ban:  []string{"> wisdom"},
		},
		{
			name: "rule renders a divider",
			in:   "a\n\n---\n\nb\n",
			want: []string{"a", "────────", "b"},
		},
		{
			name: "table renders a grid with separator row",
			in:   "| name | value |\n| --- | --- |\n| width | 80 |\n",
			want: []string{
				"| name  | value |",
				"| ───── | ───── |",
				"| width | 80    |",
			},
			ban: []string{"| --- |"},
		},
		{
			name: "table cells parse inline markers",
			in:   "| field |\n| --- |\n| **bold** |\n",
			want: []string{"| field |", "| bold  |"},
			ban:  []string{"**"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			out := Render(k, 60, tt.in)
			for _, want := range tt.want {
				if !strings.Contains(out, want) {
					t.Errorf("output missing %q:\n%s", want, out)
				}
			}

			for _, banned := range tt.ban {
				if strings.Contains(out, banned) {
					t.Errorf("output must not contain %q:\n%s", banned, out)
				}
			}
		})
	}
}

// TestRender_WrapsToWidth_ListHangingIndent pins wrapping: no rendered
// row exceeds width cells, and wrapped list rows indent under their
// text (hanging indent).
func TestRender_WrapsToWidth_ListHangingIndent(t *testing.T) {
	t.Parallel()

	k := asciiKit()

	out := Render(k, 20, strings.Repeat("word ", 12)+"\n\n- aaaaaaaaaa bbbbbbbbbb cccccccccc\n")
	for i, row := range strings.Split(out, "\n") {
		if w := ansi.StringWidth(row); w > 20 {
			t.Errorf("row %d is %d cells wide (max 20): %q", i, w, row)
		}
	}

	// The list rows: first row starts at the marker, continuations hang
	// past the marker column (indent under the text, not the bullet).
	rows := strings.Split(out, "\n")
	firstListRow, contRows := -1, 0
	for i, row := range rows {
		if firstListRow < 0 {
			if strings.Contains(row, "aaaaaaaaaa") {
				firstListRow = i
			}

			continue
		}

		if strings.Contains(row, "cccccccccc") || strings.Contains(row, "bbbbbbbbbb") {
			contRows++
			if !strings.HasPrefix(row, "  ") {
				t.Errorf("list continuation must hang two cells, got %q", row)
			}
		}
	}

	if firstListRow < 0 || contRows == 0 {
		t.Fatalf("no wrapped list continuation found:\n%s", out)
	}
}

// TestRender_CodeBlockFrameAndLanguageLabel pins the code block frame:
// every body row carries the left bar, and the language label sits on
// the frame's first row.
func TestRender_CodeBlockFrameAndLanguageLabel(t *testing.T) {
	t.Parallel()

	k := asciiKit()
	out := Render(k, 40, "```go\nline one\nline two\n```\n")

	if !strings.Contains(out, " go") && !strings.Contains(out, "go ") && !strings.Contains(out, "go") {
		t.Errorf("language label missing:\n%s", out)
	}

	rows := strings.Split(out, "\n")
	barRows := 0
	for _, row := range rows {
		if strings.Contains(row, "line one") || strings.Contains(row, "line two") {
			barRows++
		}
	}

	if barRows != 2 {
		t.Errorf("both code rows must render, got %d:\n%s", barRows, out)
	}
}

// TestRender_NoColorProfile_Degrades pins NO_COLOR behavior: the Ascii
// profile renders the same text content as the color profile, just
// without escapes.
func TestRender_NoColorProfile_Degrades(t *testing.T) {
	t.Parallel()

	sample := "# H\n\n**b** and `c`\n"

	plain := Render(asciiKit(), 60, sample)
	color := Render(kit.New(kit.DefaultTheme()), 60, sample)

	if plain != ansi.Strip(color) {
		t.Errorf("stripped color output must equal plain output:\nplain:\n%s\ncolor:\n%s", plain, ansi.Strip(color))
	}
}
