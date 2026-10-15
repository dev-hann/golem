package markdown

import (
	"strings"
	"testing"
)

// TestParse_InlineSpans_Table pins the v1 inline grammar. Unclosed
// markers render literally (the remend replacement rule), escapes lose
// their backslash, and nesting works one level deep.
func TestParse_InlineSpans_Table(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		in   string
		want []Span
	}{
		{
			name: "plain text is one span",
			in:   "hello",
			want: []Span{{Text: "hello"}},
		},
		{
			name: "bold",
			in:   "a **bold** b",
			want: []Span{{Text: "a "}, {Text: "bold", Bold: true}, {Text: " b"}},
		},
		{
			name: "italic",
			in:   "a *it* b",
			want: []Span{{Text: "a "}, {Text: "it", Italic: true}, {Text: " b"}},
		},
		{
			name: "code span",
			in:   "a `x` b",
			want: []Span{{Text: "a "}, {Text: "x", Code: true}, {Text: " b"}},
		},
		{
			name: "bold code inside each other nests",
			in:   "**a `b` c**",
			want: []Span{{Text: "a ", Bold: true}, {Text: "b", Bold: true, Code: true}, {Text: " c", Bold: true}},
		},
		{
			name: "link text and url split spans",
			in:   "see [docs](https://x) now",
			want: []Span{
				{Text: "see "},
				{Text: "docs", Link: true},
				{Text: " (https://x)", Dim: true},
				{Text: " now"},
			},
		},
		{
			name: "unclosed bold renders literally",
			in:   "a **bold",
			want: []Span{{Text: "a **bold"}},
		},
		{
			name: "unclosed code renders literally",
			in:   "a `code",
			want: []Span{{Text: "a `code"}},
		},
		{
			name: "unclosed link renders literally",
			in:   "a [text](url",
			want: []Span{{Text: "a [text](url"}},
		},
		{
			name: "escaped marker renders literally",
			in:   `a \*not italic\* b`,
			want: []Span{{Text: "a *not italic* b"}},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			got := Inline(tt.in)
			if len(got) != len(tt.want) {
				t.Fatalf("Inline(%q) = %+v, want %d spans", tt.in, got, len(tt.want))
			}

			for i := range tt.want {
				if got[i] != tt.want[i] {
					t.Errorf("span %d = %+v, want %+v", i, got[i], tt.want[i])
				}
			}
		})
	}
}

// TestInline_UnicodeTextKeepsBoundaries ensures CJK text and multibyte
// markers never split mid-rune.
func TestInline_UnicodeTextKeepsBoundaries(t *testing.T) {
	t.Parallel()

	spans := Inline("한글 **텍스트** 확인")
	if len(spans) != 3 {
		t.Fatalf("spans = %+v, want [text, bold, text]", spans)
	}

	if !spans[1].Bold || spans[1].Text != "텍스트" {
		t.Errorf("bold span = %+v, want 텍스트 bold", spans[1])
	}
}

// TestInline_LiteralGarbageNeverPanics throws hostile inputs at the
// parser; the contract is only "no panic, no lost text".
func TestInline_LiteralGarbageNeverPanics(t *testing.T) {
	t.Parallel()

	for _, in := range []string{"**", "*`*[", "`", "[a](", "[]( )", "**a*b*c**", strings.Repeat("*", 50)} {
		spans := Inline(in)

		var all strings.Builder
		for _, s := range spans {
			all.WriteString(s.Text)
		}

		if strings.ContainsRune(all.String(), '\x00') {
			t.Errorf("Inline(%q) produced NUL bytes", in)
		}
	}
}
