package tui

import (
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/charmbracelet/x/ansi"

	"github.com/dev-hann/golem/internal/tui/kit"
	"github.com/dev-hann/golem/internal/tui/markdown"
)

// testKit builds the default kit. v2 styles always render full color,
// so plain-text assertions strip escapes first (plain helper below).
func testKit() *kit.Kit { return kit.New(kit.DefaultTheme()) }

// plain strips styling so substring assertions hold regardless of the
// terminal profile in effect.
func plain(s string) string { return ansi.Strip(s) }

// testMarkdown builds the streaming markdown renderer for block tests.
func testMarkdown() *markdown.Renderer { return newMarkdownRenderer() }

func TestBlockRender_UserAndAssistant(t *testing.T) {
	t.Parallel()
	if got := plain((block{kind: blockUser, text: "hello"}).render(testKit(), testMarkdown(), 80)); got != "▎ hello" {
		t.Errorf("user block should be a user-colored bar line, got %q", got)
	}
	if got := plain((block{kind: blockAssistant}).render(testKit(), testMarkdown(), 80)); got != "…" {
		t.Errorf("empty assistant block should be a bare ellipsis, got %q", got)
	}
	if got := plain((block{kind: blockAssistant, text: "hi"}).render(testKit(), testMarkdown(), 80)); got != "hi" {
		t.Errorf("assistant block should be plain text without a bar, got %q", got)
	}
	if got := plain((block{kind: blockError, text: "boom"}).render(testKit(), testMarkdown(), 80)); !strings.Contains(got, "error: boom") {
		t.Errorf("error block = %q", got)
	}
}

func TestBlockRender_ToolStatuses(t *testing.T) {
	t.Parallel()
	running := block{kind: blockTool, tool: "bash", args: `{"command":"ls"}`}
	if got := plain(running.render(testKit(), testMarkdown(), 80)); !strings.Contains(got, "… bash") {
		t.Errorf("running tool = %q", got)
	}
	done := block{kind: blockTool, tool: "read", done: true, ok: true}
	if got := plain(done.render(testKit(), testMarkdown(), 80)); !strings.Contains(got, "✓ read") {
		t.Errorf("ok tool = %q", got)
	}
	failed := block{kind: blockTool, tool: "edit", done: true}
	if got := plain(failed.render(testKit(), testMarkdown(), 80)); !strings.Contains(got, "✗ edit") {
		t.Errorf("failed tool = %q", got)
	}
}

func TestPreview_TruncatesAtBoundary(t *testing.T) {
	t.Parallel()
	if got := preview("short", 10); got != "short" {
		t.Errorf("under limit changed: %q", got)
	}
	long := strings.Repeat("a", 300)
	got := preview(long, 100)
	if len(got) != 103 { // 100 bytes + 3-byte ellipsis rune
		t.Errorf("truncated length = %d, want 103", len(got))
	}
	if !strings.HasSuffix(got, "…") {
		t.Errorf("truncated preview missing ellipsis: %q", got)
	}
}

func TestRenderBlocks_JoinsAllBlocks(t *testing.T) {
	t.Parallel()
	got, spans, _ := renderBlocks(testKit(), testMarkdown(), []block{
		{kind: blockUser, text: "q"},
		{kind: blockAssistant, text: "a"},
		{kind: blockTool, tool: "read", done: true, ok: true},
		{kind: blockError, text: "bad"},
	}, 80)
	for _, want := range []string{"▎ q", "a", "✓ read", "error: bad"} {
		if !strings.Contains(plain(got), want) {
			t.Errorf("renderBlocks missing %q in:\n%s", want, got)
		}
	}
	// The user bar is a single line at the top of the transcript.
	if len(spans) != 1 || spans[0].start != 0 || spans[0].end != 1 || spans[0].text != "q" {
		t.Errorf("user spans = %+v, want one span [0,1) with text q:\n%s", spans, got)
	}
}

func TestRenderBlocks_NoUserBlockNoSpans(t *testing.T) {
	t.Parallel()
	if _, spans, _ := renderBlocks(testKit(), testMarkdown(), []block{{kind: blockAssistant, text: "a"}}, 80); len(spans) != 0 {
		t.Errorf("spans = %+v, want none without a user block", spans)
	}
}

func TestRenderBlocks_BlankLineBetweenBlocks(t *testing.T) {
	t.Parallel()
	got, _, _ := renderBlocks(testKit(), testMarkdown(), []block{
		{kind: blockUser, text: "q"},
		{kind: blockAssistant, text: "a"},
		{kind: blockTool, tool: "read", done: true, ok: true},
	}, 80)
	// A blank line separates the user bar from the plain assistant text;
	// tool lines attach to the block they follow without a blank line.
	if !strings.Contains(plain(got), "▎ q\n\na\n  ▸ ✓ read") {
		t.Errorf("blocks should be separated by blank lines, tool lines attached:\n%s", got)
	}
}

// assertRowsWithin fails when any display row of got exceeds width cells.
func assertRowsWithin(t *testing.T, got string, width int) {
	t.Helper()

	for i, line := range strings.Split(got, "\n") {
		if w := ansi.StringWidth(line); w > width {
			t.Errorf("row %d width %d exceeds %d:\n%s", i, w, width, got)

			return
		}
	}
}

func TestBlockRender_WrapsToViewportWidth(t *testing.T) {
	t.Parallel()

	const width = 20

	tests := []struct {
		name  string
		block block
	}{
		{name: "assistant ascii", block: block{kind: blockAssistant, text: strings.Repeat("word ", 15)}},
		{name: "assistant cjk unbroken", block: block{kind: blockAssistant, text: strings.Repeat("한글", 40)}},
		{name: "error", block: block{kind: blockError, text: strings.Repeat("bad thing ", 10)}},
		{name: "tool summary running", block: block{kind: blockTool, tool: "bash", args: `{"command":"` + strings.Repeat("ls -la ", 8) + `"}`}},
		{name: "tool preview expanded", block: block{
			kind: blockTool, tool: "bash", done: true, ok: true, expanded: true,
			result: strings.Repeat("output ", 20),
		}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			assertRowsWithin(t, tt.block.render(testKit(), testMarkdown(), width), width)
		})
	}
}

func TestBlockRender_ToolPreviewGutterAligned(t *testing.T) {
	t.Parallel()

	blk := block{kind: blockTool, tool: "bash", done: true, ok: true, expanded: true, result: strings.Repeat("x", 60)}
	lines := strings.Split(plain(blk.render(testKit(), testMarkdown(), 20)), "\n")

	if len(lines) < 3 {
		t.Fatalf("wrapped preview must span several rows, got %d:\n%s", len(lines), strings.Join(lines, "\n"))
	}

	for i, line := range lines[1:] {
		if !strings.HasPrefix(line, "  │ ") {
			t.Errorf("preview row %d missing gutter prefix:\n%s", i, strings.Join(lines, "\n"))
		}
	}
}

func TestPreview_CutRespectsRuneBoundary(t *testing.T) {
	t.Parallel()

	s := strings.Repeat("가", 700) // 3-byte runes: byte 2000 lands mid-glyph (666 runes = 1998 bytes)
	got := preview(s, 2000)

	if !utf8.ValidString(got) {
		t.Errorf("preview split a multi-byte rune: %q", got)
	}

	if !strings.HasSuffix(got, "…") {
		t.Errorf("truncated preview missing ellipsis: %q", got)
	}
}

func TestRenderBlocks_WrappedRowsCountedInSpans(t *testing.T) {
	t.Parallel()

	_, spans, _ := renderBlocks(testKit(), testMarkdown(), []block{
		{kind: blockUser, text: "q"},
		{kind: blockAssistant, text: strings.Repeat("x", 100)},
		{kind: blockUser, text: "r"},
	}, 20)

	// 1 user row + 1 blank + 5 wrapped rows (100/20) + 1 blank: the second
	// user span must start at row 8 — wrapping may not desync click spans.
	if len(spans) != 2 || spans[1].start != 8 {
		t.Errorf("spans = %+v, want second user span starting at 8", spans)
	}
}

func TestRenderBlocks_TracksEveryUserBlock(t *testing.T) {
	t.Parallel()
	got, spans, _ := renderBlocks(testKit(), testMarkdown(), []block{
		{kind: blockUser, text: "first"},
		{kind: blockAssistant, text: strings.Repeat("x", 100)},
		{kind: blockUser, text: "second"},
	}, 80)
	_ = got
	if len(spans) != 2 {
		t.Fatalf("spans = %+v, want one per user block", spans)
	}
	if spans[0].text != "first" || spans[1].text != "second" {
		t.Errorf("span texts = %+v", spans)
	}
	if spans[1].start < spans[0].end {
		t.Errorf("second span must sit past the first exchange: %+v", spans)
	}
}
