package tui

import (
	"strings"
	"testing"
)

func TestBlockRender_UserAndAssistant(t *testing.T) {
	t.Parallel()
	if got := (block{kind: blockUser, text: "hello"}).render(); !strings.Contains(got, "you> hello") {
		t.Errorf("user block = %q", got)
	}
	if got := (block{kind: blockAssistant}).render(); !strings.Contains(got, "…") {
		t.Errorf("empty assistant block should show ellipsis, got %q", got)
	}
	if got := (block{kind: blockAssistant, text: "hi"}).render(); !strings.Contains(got, "golem> hi") {
		t.Errorf("assistant block = %q", got)
	}
	if got := (block{kind: blockError, text: "boom"}).render(); !strings.Contains(got, "error: boom") {
		t.Errorf("error block = %q", got)
	}
}

func TestBlockRender_ToolStatuses(t *testing.T) {
	t.Parallel()
	running := block{kind: blockTool, tool: "bash", args: `{"command":"ls"}`}
	if got := running.render(); !strings.Contains(got, "… bash") {
		t.Errorf("running tool = %q", got)
	}
	done := block{kind: blockTool, tool: "read", done: true, ok: true}
	if got := done.render(); !strings.Contains(got, "✓ read") {
		t.Errorf("ok tool = %q", got)
	}
	failed := block{kind: blockTool, tool: "edit", done: true}
	if got := failed.render(); !strings.Contains(got, "✗ edit") {
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
	got := renderBlocks([]block{
		{kind: blockUser, text: "q"},
		{kind: blockAssistant, text: "a"},
		{kind: blockTool, tool: "read", done: true, ok: true},
		{kind: blockError, text: "bad"},
	})
	for _, want := range []string{"you> q", "golem> a", "✓ read", "error: bad"} {
		if !strings.Contains(got, want) {
			t.Errorf("renderBlocks missing %q in:\n%s", want, got)
		}
	}
}
