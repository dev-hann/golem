package tui

import (
	"strings"
	"testing"

	"github.com/dev-hann/golem/internal/tui/kit"
)

// testKit builds the default kit; the test profile is Ascii, so plain
// substring assertions hold.
func testKit() *kit.Kit { return kit.New(kit.DefaultTheme()) }

func TestBlockRender_UserAndAssistant(t *testing.T) {
	t.Parallel()
	if got := (block{kind: blockUser, text: "hello"}).render(testKit(), 80); got != "▎ hello" {
		t.Errorf("user block should be a user-colored bar line, got %q", got)
	}
	if got := (block{kind: blockAssistant}).render(testKit(), 80); got != "…" {
		t.Errorf("empty assistant block should be a bare ellipsis, got %q", got)
	}
	if got := (block{kind: blockAssistant, text: "hi"}).render(testKit(), 80); got != "hi" {
		t.Errorf("assistant block should be plain text without a bar, got %q", got)
	}
	if got := (block{kind: blockError, text: "boom"}).render(testKit(), 80); !strings.Contains(got, "error: boom") {
		t.Errorf("error block = %q", got)
	}
}

func TestBlockRender_ToolStatuses(t *testing.T) {
	t.Parallel()
	running := block{kind: blockTool, tool: "bash", args: `{"command":"ls"}`}
	if got := running.render(testKit(), 80); !strings.Contains(got, "… bash") {
		t.Errorf("running tool = %q", got)
	}
	done := block{kind: blockTool, tool: "read", done: true, ok: true}
	if got := done.render(testKit(), 80); !strings.Contains(got, "✓ read") {
		t.Errorf("ok tool = %q", got)
	}
	failed := block{kind: blockTool, tool: "edit", done: true}
	if got := failed.render(testKit(), 80); !strings.Contains(got, "✗ edit") {
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
	got, lastUserEnd := renderBlocks(testKit(), []block{
		{kind: blockUser, text: "q"},
		{kind: blockAssistant, text: "a"},
		{kind: blockTool, tool: "read", done: true, ok: true},
		{kind: blockError, text: "bad"},
	}, 80)
	for _, want := range []string{"▎ q", "a", "✓ read", "error: bad"} {
		if !strings.Contains(got, want) {
			t.Errorf("renderBlocks missing %q in:\n%s", want, got)
		}
	}
	// The user bar is a single line at the top of the transcript, so the
	// span ends before line 1.
	if lastUserEnd != 1 {
		t.Errorf("lastUserEnd = %d, want 1 (single bar line):\n%s", lastUserEnd, got)
	}
}

func TestRenderBlocks_NoUserBlockReportsMinusOne(t *testing.T) {
	t.Parallel()
	if _, lastUserEnd := renderBlocks(testKit(), []block{{kind: blockAssistant, text: "a"}}, 80); lastUserEnd != -1 {
		t.Errorf("lastUserEnd = %d, want -1 without a user block", lastUserEnd)
	}
}

func TestRenderBlocks_BlankLineBetweenBlocks(t *testing.T) {
	t.Parallel()
	got, _ := renderBlocks(testKit(), []block{
		{kind: blockUser, text: "q"},
		{kind: blockAssistant, text: "a"},
		{kind: blockTool, tool: "read", done: true, ok: true},
	}, 80)
	// A blank line separates the user bar from the plain assistant text;
	// tool lines attach to the block they follow without a blank line.
	if !strings.Contains(got, "▎ q\n\na\n  ✓ read") {
		t.Errorf("blocks should be separated by blank lines, tool lines attached:\n%s", got)
	}
}

func TestRenderBlocks_TracksLastUserBlock(t *testing.T) {
	t.Parallel()
	got, lastUserEnd := renderBlocks(testKit(), []block{
		{kind: blockUser, text: "first"},
		{kind: blockAssistant, text: strings.Repeat("x", 100)},
		{kind: blockUser, text: "second"},
	}, 80)
	_ = got
	if lastUserEnd <= 4 {
		t.Errorf("lastUserEnd = %d, should track the second user block past the first exchange", lastUserEnd)
	}
}
