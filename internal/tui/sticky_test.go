package tui

import (
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/dev-hann/golem/pkg/golemclient"
)

// Sticky push-out semantics (ADR-0028): the pinned line holds the latest
// user message pushed above the rendered window's top. A newly submitted
// question does NOT clear it — the pin swaps only when streamed content
// pushes the new question's bar past the window top.

// stickyAnswerLines sizes the fixture answers: long enough to push the
// question bar past the top on an 80x24 screen, short enough to render
// trivially.
const stickyAnswerLines = 60

// stickyEarlyDeltas is the short streamed answer that stays on screen
// under the pinned line while the new question's bar is still visible.
const stickyEarlyDeltas = 3

// stickyTranscript builds a connected, session-bound model at 80x24 with
// one answered question: the question bar sits above a long answer, the
// turn has ended, and the viewport rests at the bottom — the pin is the
// only on-screen copy of the question.
func stickyTranscript(t *testing.T) Model {
	t.Helper()

	m, _ := wired(t)
	m = resize(m, 80, 24)
	m = deliver(t, m, golemclient.SessionMeta{
		Type: golemclient.TypeSessionMeta, Session: "s1",
	})
	m.blocks = []block{
		{kind: blockUser, text: "question one"},
		{kind: blockAssistant, text: strings.Repeat("answer line\n", stickyAnswerLines)},
	}
	m.refreshView()
	m = deliver(t, m, golemclient.TurnStart{Type: golemclient.TypeTurnStart})
	m = deliver(t, m, golemclient.TurnEnd{Type: golemclient.TypeTurnEnd})

	return m
}

// lineCount reports a view's row count.
func lineCount(view string) int {
	return strings.Count(view, "\n") + 1
}

// TestSticky_HoldsPinnedQuestionAcrossSubmit is the regression test for
// the vanish bug: submitting the next question must not clear the pin —
// not at submit, not at the turn-start follow snap, and not while the
// short streamed answer keeps the new bar on screen.
func TestSticky_HoldsPinnedQuestionAcrossSubmit(t *testing.T) {
	t.Parallel()

	m := stickyTranscript(t)

	if got := m.stickyText(); got != "question one" {
		t.Fatalf("precondition: sticky must pin question one, got %q", got)
	}
	before := lineCount(m.View())

	// Submit the next question through the real composer path.
	m.input.SetValue("question two")
	m = key(t, m, tea.KeyMsg{Type: tea.KeyEnter})

	// The server echoes turn_start, whose follow snap to the bottom was
	// the exact moment the old rule dropped the pin.
	m = deliver(t, m, golemclient.TurnStart{Type: golemclient.TypeTurnStart})

	if got := m.stickyText(); got != "question one" {
		t.Fatalf("turn start must not clear the pin: sticky = %q, want question one", got)
	}
	view := m.View()
	if !strings.Contains(view, "▎ question one") {
		t.Errorf("the pinned line must keep rendering after submit:\n%s", view)
	}
	if !strings.Contains(view, "▎ question two") {
		t.Errorf("the submitted question's bar must render in the transcript:\n%s", view)
	}
	if n := strings.Count(view, "▎ question one"); n != 1 {
		t.Errorf("pinned question should render once (the sticky copy), found %d:\n%s", n, view)
	}
	if after := lineCount(view); after != before {
		t.Errorf("layout height jumped across submit: before=%d after=%d", before, after)
	}

	// A short streamed answer keeps the new bar on screen: the pin holds
	// until content pushes that bar past the window top.
	for range stickyEarlyDeltas {
		m = deliver(t, m, golemclient.TextDelta{
			Type: golemclient.TypeTextDelta, Delta: "early reply line\n",
		})
	}

	if got := m.stickyText(); got != "question one" {
		t.Fatalf("pin must hold while the new bar is visible, got %q", got)
	}
	if !strings.Contains(m.View(), "▎ question two") {
		t.Errorf("the new bar must stay on screen through the early deltas:\n%s", m.View())
	}
}

// TestSticky_SwapsWhenPushedPastWindowTop pins down the swap moment: the
// pin cedes to the new question exactly when the rendered window's top
// passes the new bar's first line — not before, not after.
func TestSticky_SwapsWhenPushedPastWindowTop(t *testing.T) {
	t.Parallel()

	m := stickyTranscript(t)
	m.input.SetValue("question two")
	m = key(t, m, tea.KeyMsg{Type: tea.KeyEnter})
	m = deliver(t, m, golemclient.TurnStart{Type: golemclient.TypeTurnStart})

	span := m.userSpans[len(m.userSpans)-1]

	// Stream one line at a time and watch the window top close in.
	for i := range stickyAnswerLines {
		m = deliver(t, m, golemclient.TextDelta{
			Type: golemclient.TypeTextDelta, Delta: "push line\n",
		})

		top, _ := m.renderedWindow()
		got := m.stickyText()

		want := "question one"
		if top > span.start {
			want = "question two" // the bar crossed above the top this delta
		}

		if got != want {
			t.Fatalf("delta %d: top=%d span.start=%d sticky=%q, want %q",
				i, top, span.start, got, want)
		}

		if got == "question two" {
			// The swap happened: assert the push-out shape and stop —
			// further deltas cannot change the outcome.
			view := m.View()
			if n := strings.Count(view, "▎"); n != 1 {
				t.Errorf("exactly one bar line should render, found %d:\n%s", n, view)
			}
			if strings.Contains(view, "question one") {
				t.Errorf("the old pin should be pushed out of the view:\n%s", view)
			}

			return
		}
	}

	t.Fatal("streaming never pushed the new question past the window top")
}

// TestSticky_SwapBoundaryAtWindowTop walks the exact boundary with
// manual scroll offsets: the pin names the latest question whose bar
// starts ABOVE the rendered window's top row; a bar on or below the top
// row is transcript content, not a pin.
func TestSticky_SwapBoundaryAtWindowTop(t *testing.T) {
	t.Parallel()

	m, _ := wired(t)
	m = resize(m, 80, 24)
	m.blocks = []block{
		{kind: blockUser, text: "question one"},
		{kind: blockAssistant, text: strings.Repeat("answer line\n", stickyAnswerLines)},
		{kind: blockUser, text: "question two"},
		{kind: blockAssistant, text: strings.Repeat("answer line\n", stickyAnswerLines)},
	}
	m.refreshView()

	q1 := m.userSpans[0].start
	q2 := m.userSpans[1].start

	tests := []struct {
		name   string
		offset int
		want   string
	}{
		{"first question visible at the top", q1, ""},
		{"inside the first answer", q1 + 10, "question one"},
		{"second bar one row into the window", q2 - 1, "question one"},
		{"second bar on the window's top row", q2, "question one"},
		{"second bar pushed above the window", q2 + 1, "question two"},
		{"inside the second answer", q2 + 10, "question two"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			// Copy per subtest: the closure would otherwise share one
			// model variable across parallel writers (data race).
			m := m
			m.vp.YOffset = tt.offset

			if got := m.stickyText(); got != tt.want {
				t.Errorf("offset=%d: sticky = %q, want %q", tt.offset, got, tt.want)
			}
		})
	}
}
