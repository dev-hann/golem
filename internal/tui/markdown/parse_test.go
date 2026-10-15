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
