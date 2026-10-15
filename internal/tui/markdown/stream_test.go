package markdown

import (
	"strings"
	"testing"

	"github.com/charmbracelet/lipgloss"
	"github.com/muesli/termenv"

	"github.com/dev-hann/golem/internal/tui/kit"
)

// countingRenderer counts Render calls per exact input so tests can
// assert the completed/tail caching contract.
type countingRenderer struct {
	k      *kit.Kit
	calls  map[string]int
	render func(k *kit.Kit, width int, text string) string
}

func newCountingRenderer() *countingRenderer {
	r := lipgloss.NewRenderer(nil)
	r.SetColorProfile(termenv.Ascii)

	return &countingRenderer{
		k:      kit.NewWithRenderer(kit.DefaultTheme(), r),
		calls:  map[string]int{},
		render: Render,
	}
}

func (c *countingRenderer) count(k *kit.Kit, width int, text string) string {
	c.calls[text]++

	return c.render(k, width, text)
}

// TestStream_CompletedBlocksCached_TailOnlyReparsed pins the streaming
// cost bound (ADR-0026): as deltas append to a message, the completed
// prefix renders once per width; subsequent deltas re-render only the
// live tail.
func TestStream_CompletedBlocksCached_TailOnlyReparsed(t *testing.T) {
	t.Parallel()

	cr := newCountingRenderer()
	r := NewRenderer(cr.count)

	var text string

	for _, delta := range []string{
		"# Title\n\n",
		"first paragraph done\n\n",
		"second ",
		"paragraph ",
		"still streaming",
	} {
		text += delta
		r.Render(cr.k, 60, text)
	}

	// After the first delta the prefix "# Title\n\n" is completed: it
	// renders exactly once no matter how many deltas follow.
	if got := cr.calls["# Title\n\n"]; got != 1 {
		t.Errorf("completed prefix rendered %d times, want 1 (calls: %+v)", got, cr.calls)
	}

	// After the second delta the prefix grows to two completed blocks;
	// that longer prefix also renders exactly once.
	if got := cr.calls["# Title\n\nfirst paragraph done\n\n"]; got != 1 {
		t.Errorf("grown prefix rendered %d times, want 1 (calls: %+v)", got, cr.calls)
	}

	// The live tail re-renders on every delta — that is the bounded
	// cost, not the whole message.
	if got := cr.calls["second paragraph still streaming"]; got != 1 {
		t.Errorf("final tail rendered %d times, want 1 (calls: %+v)", got, cr.calls)
	}

	if got := cr.calls["second "]; got != 1 {
		t.Errorf("partial tail rendered %d times, want 1 (calls: %+v)", got, cr.calls)
	}
}

// TestStream_OutputMatchesFullRender pins the equivalence contract:
// incremental rendering produces exactly what a from-scratch full
// render produces.
func TestStream_OutputMatchesFullRender(t *testing.T) {
	t.Parallel()

	cr := newCountingRenderer()
	r := NewRenderer(cr.count)

	text := "# H\n\npara one\n\n- a\n- b\n\n```go\ncode\n```\nfinal"

	var (
		incremental string
		built       string
	)

	for _, chunk := range strings.SplitAfter(text, "\n\n") {
		built += chunk
		incremental = r.Render(cr.k, 60, built)
	}

	if got, want := incremental, Render(cr.k, 60, text); got != want {
		t.Errorf("incremental render diverged:\ngot:\n%s\nwant:\n%s", got, want)
	}
}

// TestStream_UnclosedEmphasis_LiteralOutput pins the remend replacement
// rule: a bold marker that never closes renders its asterisks literally
// mid-stream, and closes properly once the tail arrives.
func TestStream_UnclosedEmphasis_LiteralOutput(t *testing.T) {
	t.Parallel()

	cr := newCountingRenderer()
	r := NewRenderer(cr.count)

	mid := r.Render(cr.k, 60, "bold **word")
	if !strings.Contains(mid, "**word") {
		t.Errorf("unclosed emphasis must render literally, got:\n%s", mid)
	}

	done := r.Render(cr.k, 60, "bold **word** now\n")
	if strings.Contains(done, "**") {
		t.Errorf("closed emphasis must style, got:\n%s", done)
	}

	if !strings.Contains(done, "word") {
		t.Errorf("styled text missing:\n%s", done)
	}
}

// TestStream_TableStreaming pins the table streaming rules: rows
// arriving mid-table re-render only the live tail (cache untouched), a
// never-arriving delimiter keeps pipes literal, and the finished table
// matches a from-scratch render.
func TestStream_TableStreaming(t *testing.T) {
	t.Parallel()

	cr := newCountingRenderer()
	r := NewRenderer(cr.count)

	var text string

	for _, delta := range []string{
		"intro\n\n",
		"| a | b |\n| - | - |\n| 1 ",
		"| 2 |\n",
	} {
		text += delta
		r.Render(cr.k, 60, text)
	}

	// The intro prefix caches once.
	if got := cr.calls["intro\n\n"]; got != 1 {
		t.Errorf("intro prefix rendered %d times, want 1: %+v", got, cr.calls)
	}

	// Final text is settled: everything is prefix, and the render
	// equals a full from-scratch render.
	want := Render(cr.k, 60, text)
	if got := r.Render(cr.k, 60, text); got != want {
		t.Errorf("settled render diverged:\ngot:\n%s\nwant:\n%s", got, want)
	}
}

// TestStream_PipesWithoutDelimiterStayLiteral pins the streaming-safe
// fallback: pipes without a delimiter row render literally, and never
// flicker into a table and back.
func TestStream_PipesWithoutDelimiterStayLiteral(t *testing.T) {
	t.Parallel()

	cr := newCountingRenderer()
	r := NewRenderer(cr.count)

	for _, step := range []string{"| a", "| b", "| c"} {
		out := r.Render(cr.k, 60, step)
		if !strings.Contains(out, "|") {
			t.Errorf("pipes must stay literal, got:\n%s", out)
		}
	}
}

// TestStream_WidthChangeBustsCache pins the cache key: the same text at
// a new width re-renders (stale wraps must not survive a resize).
func TestStream_WidthChangeBustsCache(t *testing.T) {
	t.Parallel()

	cr := newCountingRenderer()
	r := NewRenderer(cr.count)

	const text = "fixed text\n\nsecond block\n"

	// settled text: everything is prefix, nothing live.
	r.Render(cr.k, 40, text)
	r.Render(cr.k, 40, text)

	if got := cr.calls[text]; got != 1 {
		t.Errorf("same text+width should cache: %d calls, want 1", got)
	}

	r.Render(cr.k, 60, text)
	r.Render(cr.k, 60, text)

	// The prefix cache key carries the width: two distinct widths mean
	// two render calls for the same prefix — 2, not 1, not 4.
	if got := cr.calls[text]; got != 2 {
		t.Errorf("same text at two widths should render twice, got %d", got)
	}

	total := 0
	for _, n := range cr.calls {
		total += n
	}

	if total != 2 {
		t.Errorf("expected exactly 2 renders (one per width), got %d: %+v", total, cr.calls)
	}
}
