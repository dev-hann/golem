package tui

import (
	"strings"
	"testing"

	"github.com/dev-hann/golem/pkg/golemclient"
)

// TestRenderBlocks_MarkdownAssistant pins the ADR-0026 integration:
// assistant blocks render markdown through the streaming renderer while
// every other block kind keeps its exact existing shape.
func TestRenderBlocks_MarkdownAssistant(t *testing.T) {
	t.Parallel()

	md := newMarkdownRenderer()
	got, _, _ := renderBlocks(testKit(), md, []block{
		{kind: blockUser, text: "# not a heading"},
		{kind: blockAssistant, text: "## Real heading\n\n- one\n- two"},
	}, 60)

	// User input stays verbatim — markdown is assistant-only.
	if !strings.Contains(got, "# not a heading") {
		t.Errorf("user text must stay verbatim:\n%s", got)
	}

	// Assistant markdown renders: markers consumed, items marked.
	if !strings.Contains(got, "Real heading") {
		t.Errorf("heading text missing:\n%s", got)
	}

	if strings.Contains(got, "##") {
		t.Errorf("heading markers must be consumed:\n%s", got)
	}

	if !strings.Contains(got, "one") || !strings.Contains(got, "two") {
		t.Errorf("list items missing:\n%s", got)
	}

	if strings.Contains(got, "- one") {
		t.Errorf("list markers must be consumed:\n%s", got)
	}
}

// TestRenderBlocks_TableMarkdown pins the ADR-0027 integration: an
// assistant table renders as a grid while user text keeps its pipes.
func TestRenderBlocks_TableMarkdown(t *testing.T) {
	t.Parallel()

	md := newMarkdownRenderer()
	got, _, _ := renderBlocks(testKit(), md, []block{
		{kind: blockUser, text: "| not | a | table |"},
		{kind: blockAssistant, text: "| h | k |\n| - | - |\n| a | 1 |"},
	}, 60)

	if !strings.Contains(got, "| not | a | table |") {
		t.Errorf("user pipes must stay verbatim:\n%s", got)
	}

	if !strings.Contains(got, "| h | k |") {
		t.Errorf("assistant table must render a grid:\n%s", got)
	}

	if strings.Contains(got, "| - ") {
		t.Errorf("delimiter row must be consumed:\n%s", got)
	}
}

// TestRenderBlocks_SpansRecomputedWithMarkdownHeight pins the geometry
// risk: markdown changes an assistant block's rendered height, and user
// spans after it must still map to their rendered rows.
func TestRenderBlocks_SpansRecomputedWithMarkdownHeight(t *testing.T) {
	t.Parallel()

	md := newMarkdownRenderer()
	blocks := []block{
		{kind: blockUser, text: "first"},
		{kind: blockAssistant, text: "para\n\n- a\n- b\n\n```go\ncode\n```"},
		{kind: blockUser, text: "second"},
	}

	_, spans, _ := renderBlocks(testKit(), md, blocks, 60)
	if len(spans) != 2 {
		t.Fatalf("spans = %+v, want 2", spans)
	}

	// The second user block must start below the assistant block's full
	// rendered height (1 + blank + code label + code row + blank).
	if spans[1].start <= spans[0].end {
		t.Errorf("second span starts at %d, must clear the markdown block (first ends %d)",
			spans[1].start, spans[0].end)
	}

	if spans[1].text != "second" {
		t.Errorf("second span text = %q", spans[1].text)
	}
}

// TestHistoryReplay_RendersMarkdown pins the replay path: a history
// replay carrying markdown in assistant messages renders styled, not
// raw.
func TestHistoryReplay_RendersMarkdown(t *testing.T) {
	t.Parallel()

	m, _ := wired(t)
	m = resize(m, 80, 24)

	m = deliver(t, m, golemclient.UserMessage{Type: golemclient.TypeUserMessage, Text: "question"})
	m = deliver(t, m, golemclient.TurnStart{Type: golemclient.TypeTurnStart})
	m = deliver(t, m, golemclient.TextDelta{Type: golemclient.TypeTextDelta, Delta: "## Answer\n\nexplanation"})
	m = deliver(t, m, golemclient.TurnEnd{Type: golemclient.TypeTurnEnd})

	if !strings.Contains(m.content, "Answer") {
		t.Errorf("replayed markdown text missing:\n%s", m.content)
	}

	if strings.Contains(m.content, "## Answer") {
		t.Errorf("replayed markdown must render styled, not raw:\n%s", m.content)
	}
}
