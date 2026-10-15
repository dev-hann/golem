package markdown

import (
	"reflect"
	"testing"
)

// TestParse_BlockKinds_Table pins the v1 block grammar: every supported
// block kind parses with its content, and unrecognized shapes stay
// paragraphs (raw fallback is the safety net, never an error).
func TestParse_BlockKinds_Table(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		in   string
		want []Block
	}{
		{
			name: "plain paragraph",
			in:   "hello world",
			want: []Block{{Kind: BlockParagraph, Text: "hello world"}},
		},
		{
			name: "h1 through h4",
			in:   "# one\n\ntwo\n\n### three\n\n#### four",
			want: []Block{
				{Kind: BlockHeading, Level: 1, Text: "one"},
				{Kind: BlockParagraph, Text: "two"},
				{Kind: BlockHeading, Level: 3, Text: "three"},
				{Kind: BlockHeading, Level: 4, Text: "four"},
			},
		},
		{
			name: "h5 and h6 fall back to one paragraph",
			in:   "##### five\n###### six",
			want: []Block{
				{Kind: BlockParagraph, Text: "##### five\n###### six"},
			},
		},
		{
			name: "closed fence is a code block with language",
			in:   "```go\nfmt.Println(1)\nfmt.Println(2)\n```",
			want: []Block{{Kind: BlockCode, Lang: "go", Text: "fmt.Println(1)\nfmt.Println(2)\n"}},
		},
		{
			name: "bare fence has no language",
			in:   "```\nplain\n```",
			want: []Block{{Kind: BlockCode, Text: "plain\n"}},
		},
		{
			name: "bullet list groups one item per line",
			in:   "- alpha\n- beta",
			want: []Block{{Kind: BlockList, Ordered: false, Items: []string{"alpha", "beta"}}},
		},
		{
			name: "ordered list",
			in:   "1. alpha\n2. beta",
			want: []Block{{Kind: BlockList, Ordered: true, Items: []string{"alpha", "beta"}}},
		},
		{
			name: "quote joins its lines",
			in:   "> quoted line\n> continued",
			want: []Block{{Kind: BlockQuote, Text: "quoted line\ncontinued"}},
		},
		{
			name: "thematic break",
			in:   "above\n\n---\n\nbelow",
			want: []Block{
				{Kind: BlockParagraph, Text: "above"},
				{Kind: BlockRule},
				{Kind: BlockParagraph, Text: "below"},
			},
		},
		{
			name: "blank line separates paragraphs",
			in:   "first\n\nsecond",
			want: []Block{
				{Kind: BlockParagraph, Text: "first"},
				{Kind: BlockParagraph, Text: "second"},
			},
		},
		{
			name: "multiline paragraph keeps line breaks",
			in:   "one\ntwo",
			want: []Block{{Kind: BlockParagraph, Text: "one\ntwo"}},
		},
		{
			name: "empty input yields no blocks",
			in:   "",
			want: nil,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			got := Parse(tt.in)
			if len(got) != len(tt.want) {
				t.Fatalf("Parse(%q) = %d blocks %+v, want %d", tt.in, len(got), got, len(tt.want))
			}

			// settled is a streaming detail verified by Split's tests;
			// block-kind comparisons clear it.
			for i := range got {
				got[i].settled = false
			}

			for i := range tt.want {
				if !reflect.DeepEqual(got[i], tt.want[i]) {
					t.Errorf("block %d = %+v, want %+v", i, got[i], tt.want[i])
				}
			}
		})
	}
}

// TestParse_LineAfterBlock_NotDropped pins a lossless-parse rule: the
// line immediately following a closed fence, a quote, or a list is its
// own block (lazy continuation is not supported, but content is never
// silently dropped).
func TestParse_LineAfterBlock_NotDropped(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		in      string
		wantLen int
		want    string // text of the block after the first
	}{
		{name: "line after closed fence", in: "```\ncode\n```\nafter", wantLen: 2, want: "after"},
		{name: "line after quote", in: "> quote\nafter", wantLen: 2, want: "after"},
		{name: "line after list", in: "- item\nafter", wantLen: 2, want: "after"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			got := Parse(tt.in)
			if len(got) != tt.wantLen {
				t.Fatalf("Parse(%q) = %d blocks %+v, want %d", tt.in, len(got), got, tt.wantLen)
			}

			if got[1].Text != tt.want {
				t.Errorf("block after = %+v, want text %q", got[1], tt.want)
			}
		})
	}
}

// TestParse_Table pins the table grammar (ADR-0027): a pipe header
// followed by a delimiter row opens a table; body rows are consecutive
// pipe lines. Without a delimiter row the pipes stay a paragraph (the
// streaming-safe fallback), and cell counts normalize to the header.
func TestParse_Table(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		in   string
		want []Block
	}{
		{
			name: "gfm table with alignment",
			in:   "| a | b | c |\n| --- | :--- | ---: |\n| 1 | 2 | 3 |",
			want: []Block{{
				Kind:  BlockTable,
				Cells: [][]string{{"a", "b", "c"}, {"1", "2", "3"}},
				Align: []Align{AlignDefault, AlignLeft, AlignRight},
			}},
		},
		{
			name: "center alignment",
			in:   "| a |\n| :---: |\n| 1 |",
			want: []Block{{
				Kind:  BlockTable,
				Cells: [][]string{{"a"}, {"1"}},
				Align: []Align{AlignCenter},
			}},
		},
		{
			name: "pipe line without delimiter stays a paragraph",
			in:   "| not a table",
			want: []Block{{Kind: BlockParagraph, Text: "| not a table"}},
		},
		{
			name: "header and delimiter alone form an empty table",
			in:   "| a |\n| - |",
			want: []Block{{
				Kind:  BlockTable,
				Cells: [][]string{{"a"}},
				Align: []Align{AlignDefault},
			}},
		},
		{
			name: "cell counts normalize to the header",
			in:   "| a | b |\n| - | - |\n| 1 | 2 | extra |",
			want: []Block{{
				Kind:  BlockTable,
				Cells: [][]string{{"a", "b"}, {"1", "2"}},
				Align: []Align{AlignDefault, AlignDefault},
			}},
		},
		{
			name: "short rows pad with empty cells",
			in:   "| a | b |\n| - | - |\n| only |",
			want: []Block{{
				Kind:  BlockTable,
				Cells: [][]string{{"a", "b"}, {"only", ""}},
				Align: []Align{AlignDefault, AlignDefault},
			}},
		},
		{
			name: "cells trim surrounding spaces",
			in:   "|  a  |  b  |\n| - | - |\n|  1  |  2  |",
			want: []Block{{
				Kind:  BlockTable,
				Cells: [][]string{{"a", "b"}, {"1", "2"}},
				Align: []Align{AlignDefault, AlignDefault},
			}},
		},
		{
			name: "row consumption stops at a non-pipe line",
			in:   "| a |\n| - |\n| 1 |\nafter",
			want: []Block{
				{Kind: BlockTable, Cells: [][]string{{"a"}, {"1"}}, Align: []Align{AlignDefault}},
				{Kind: BlockParagraph, Text: "after"},
			},
		},
		{
			name: "delimiter mismatch falls back to paragraph",
			in:   "| a |\n| x |\n| 1 |",
			want: []Block{{Kind: BlockParagraph, Text: "| a |\n| x |\n| 1 |"}},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			got := Parse(tt.in)
			if len(got) != len(tt.want) {
				t.Fatalf("Parse(%q) = %d blocks %+v, want %d", tt.in, len(got), got, len(tt.want))
			}

			for i := range got {
				got[i].settled = false
			}

			for i := range tt.want {
				if !reflect.DeepEqual(got[i], tt.want[i]) {
					t.Errorf("block %d = %+v, want %+v", i, got[i], tt.want[i])
				}
			}
		})
	}
}

// TestParse_TrailingTableLive pins the streaming rule for tables: a
// table whose last row has not seen its newline stays live (the row may
// still grow); a trailing newline settles the whole table.
func TestParse_TrailingTableLive(t *testing.T) {
	t.Parallel()

	live := Parse("intro\n\n| a | b |\n| - | - |\n| partial")
	if len(live) != 2 || live[1].Kind != BlockTable {
		t.Fatalf("blocks = %+v, want paragraph + live table", live)
	}

	done, tail := Split(live)
	if len(done) != 1 || len(tail) != 1 || tail[0].Kind != BlockTable {
		t.Errorf("Split = %d completed, %+v live, want the partial table live", len(done), tail)
	}

	settled := Parse("intro\n\n| a | b |\n| - | - |\n| 1 |\n")
	done, tail = Split(settled)
	if len(done) != 2 || len(tail) != 0 {
		t.Errorf("Split = %d completed, %+v live, want everything completed", len(done), tail)
	}
}

// TestParse_OpenFence_IsCodeBlock pins the streaming rule: from an
// unclosed fence to the end of input, everything is code — never a
// half-styled paragraph.
func TestParse_OpenFence_IsCodeBlock(t *testing.T) {
	t.Parallel()

	got := Parse("intro\n\n```go\nfmt.Println(")
	if len(got) != 2 {
		t.Fatalf("blocks = %+v, want intro + one code block", got)
	}

	if got[0].Kind != BlockParagraph || got[0].Text != "intro" {
		t.Errorf("first block = %+v, want paragraph intro", got[0])
	}

	if got[1].Kind != BlockCode || got[1].Lang != "go" || got[1].Text != "fmt.Println(\n" {
		t.Errorf("open fence block = %+v, want go code with partial text", got[1])
	}

	// The open fence is live: the closing fence has not arrived.
	completed, live := Split(got)
	if len(completed) != 1 || len(live) != 1 || live[0].Kind != BlockCode {
		t.Errorf("Split = %d completed, %+v live, want the open fence live", len(completed), live)
	}
}

// TestParse_TrailingUnclosedBlocksAreLive pins the completed/tail split:
// only the final block may be incomplete (open fence, or a block whose
// closing blank line has not streamed yet). Completed returns the prefix
// safe to cache; Live returns the rest.
func TestParse_TrailingUnclosedBlocksAreLive(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name       string
		in         string
		wantDone   int
		wantLive   int
		liveIsCode bool
	}{
		{name: "settled text is all completed", in: "a\n\nb\n", wantDone: 2},
		{name: "paragraph without trailing newline stays live", in: "a\n\npartial", wantDone: 1, wantLive: 1},
		{
			name: "open fence is live code", in: "a\n\n```go\ncode", wantDone: 1, wantLive: 1,
			liveIsCode: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			done, live := Split(Parse(tt.in))
			if len(done) != tt.wantDone {
				t.Errorf("completed blocks = %d (%+v), want %d", len(done), done, tt.wantDone)
			}

			if len(live) != tt.wantLive {
				t.Fatalf("live blocks = %d (%+v), want %d", len(live), live, tt.wantLive)
			}

			if tt.liveIsCode && (len(live) != 1 || live[0].Kind != BlockCode) {
				t.Errorf("live block = %+v, want one code block", live)
			}
		})
	}
}
