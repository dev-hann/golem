package tui

import (
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/dev-hann/golem/pkg/golemclient"
)

// leaderKey presses the leader then a printable key (ctrl+x <r>). The
// arm's tick cmd stays unrun — executing it would stall the test for
// the leader timeout window.
func leaderKey(t *testing.T, m Model, r rune) Model {
	t.Helper()

	m = arm(m)

	return key(t, m, rkey(r))
}

// enterKey is the plain enter press used to commit picker inputs.
func enterKey() tea.KeyMsg {
	return tea.KeyMsg{Type: tea.KeyEnter}
}

// deliverMeta is a one-liner for folding a session_meta event with the
// fields the header cares about.
func deliverMeta(t *testing.T, m Model, title, cwd string) Model {
	t.Helper()

	return deliver(t, m, golemclient.SessionMeta{
		Type: golemclient.TypeSessionMeta, Session: "s1", Title: title, Cwd: cwd,
	})
}

// headerLineOf returns the top content row of the chat screen — the row
// below the screen's top padding (and the header when it renders).
func headerLineOf(m Model) string {
	lines := strings.Split(m.View(), "\n")

	return lines[screenPadV]
}

// TestModel_SessionHeaderShowsTitleAndDir pins the header's contract:
// a titled session shows its title and working-directory tail on the
// first content row of the screen.
func TestModel_SessionHeaderShowsTitleAndDir(t *testing.T) {
	t.Parallel()

	m, _ := wired(t)
	m = resize(m, 80, 24)
	m = deliverMeta(t, m, "Fix auth middleware", "/work/golem")

	line := headerLineOf(m)
	if !strings.Contains(line, "Fix auth middleware") {
		t.Errorf("header must show the session title, got:\n%s", m.View())
	}

	if !strings.Contains(line, "golem") {
		t.Errorf("header must show the cwd tail, got:\n%s", m.View())
	}
}

// TestModel_SessionHeaderPlaceholderUntilTitled: an untitled session
// renders a dim placeholder, not an empty bar.
func TestModel_SessionHeaderPlaceholderUntilTitled(t *testing.T) {
	t.Parallel()

	m, _ := wired(t)
	m = resize(m, 80, 24)
	m = deliverMeta(t, m, "", "/work/golem")

	if line := headerLineOf(m); !strings.Contains(line, "untitled") {
		t.Errorf("untitled session must show a placeholder, got:\n%s", m.View())
	}
}

// TestModel_SessionHeaderNoSessionIsDimPlaceholder: before any session
// exists (lazy create, ADR-0018) the header names that state instead of
// pretending.
func TestModel_SessionHeaderNoSessionIsDimPlaceholder(t *testing.T) {
	t.Parallel()

	m, _ := wired(t)
	m = resize(m, 80, 24)

	if line := headerLineOf(m); !strings.Contains(line, "no session") {
		t.Errorf("sessionless header must show a placeholder, got:\n%s", m.View())
	}
}

// TestModel_SessionTitleEventUpdatesHeader: a session_title event for
// the current session refreshes the header even when the session never
// appeared in a session_list on this client (generation races the list).
func TestModel_SessionTitleEventUpdatesHeader(t *testing.T) {
	t.Parallel()

	m, _ := wired(t)
	m = resize(m, 80, 24)
	m = deliverMeta(t, m, "", "/w")

	m = deliver(t, m, golemclient.SessionTitle{
		Type: golemclient.TypeSessionTitle, Session: "s1", Title: "Server title",
	})

	if line := headerLineOf(m); !strings.Contains(line, "Server title") {
		t.Errorf("session_title event must refresh the header:\n%s", m.View())
	}
}

// TestModel_SessionHeaderResetsOnNewSession: ctrl+x n clears the header
// back to the sessionless placeholder (the old session's title must not
// outlive the switch).
func TestModel_SessionHeaderResetsOnNewSession(t *testing.T) {
	t.Parallel()

	m, _ := wired(t)
	m = resize(m, 80, 24)
	m = deliverMeta(t, m, "Old title", "/w")

	m = leaderKey(t, m, 'n')

	if line := headerLineOf(m); strings.Contains(line, "Old title") {
		t.Errorf("header must reset on new session:\n%s", m.View())
	}
}

// TestModel_SessionHeaderRenameUpdatesImmediately: committing a rename
// in the picker updates the header at once (optimistic — the server's
// session_title reply reconciles the list).
func TestModel_SessionHeaderRenameUpdatesImmediately(t *testing.T) {
	t.Parallel()

	m, fc := wired(t)
	m = resize(m, 80, 24)
	m = deliverMeta(t, m, "", "/w")
	m.listRequest = true
	m = deliver(t, m, golemclient.SessionList{
		Type: golemclient.TypeSessionList,
		Sessions: []golemclient.SessionSummary{
			{ID: "s1", Title: "Fix auth", Cwd: "/a"},
			{ID: "s2", Title: "Elsewhere", Cwd: "/b"},
		},
	})

	m = key(t, m, tea.KeyMsg{Type: tea.KeyCtrlR}) // rename the highlighted row
	for _, r := range "New name" {
		m = key(t, m, rkey(r))
	}
	m = key(t, m, enterKey())
	m = key(t, m, tea.KeyMsg{Type: tea.KeyEscape}) // close the picker

	if line := headerLineOf(m); !strings.Contains(line, "New name") {
		t.Errorf("rename must update the header immediately:\n%s", m.View())
	}

	if len(fc.sent) == 0 {
		t.Error("rename must still send rename_session")
	}
}

// TestModel_SessionHeaderHiddenOnTinyTerminal: a terminal too short for
// the header drops it rather than overflowing the screen height.
func TestModel_SessionHeaderHiddenOnTinyTerminal(t *testing.T) {
	t.Parallel()

	m, _ := wired(t)
	m = resize(m, 24, 10)
	m = deliverMeta(t, m, "Fix auth", "/w")

	view := m.View()
	if got := strings.Count(view, "\n") + 1; got > m.height {
		t.Errorf("tiny terminal height = %d rows, exceeds terminal %d:\n%s", got, m.height, view)
	}

	if strings.Contains(view, "Fix auth") {
		t.Errorf("header must be dropped on a tiny terminal:\n%s", view)
	}
}

// TestModel_SessionHeaderHeightConstant: the screen height never jumps
// between the sessionless, untitled, and titled states.
func TestModel_SessionHeaderHeightConstant(t *testing.T) {
	t.Parallel()

	m, _ := wired(t)
	m = resize(m, 80, 24)

	heights := make([]int, 0, 3)
	heights = append(heights, strings.Count(m.View(), "\n")+1)

	m = deliverMeta(t, m, "", "/w")
	heights = append(heights, strings.Count(m.View(), "\n")+1)

	m = deliver(t, m, golemclient.SessionTitle{
		Type: golemclient.TypeSessionTitle, Session: "s1", Title: "Now titled",
	})
	heights = append(heights, strings.Count(m.View(), "\n")+1)

	for _, h := range heights {
		if h != heights[0] {
			t.Errorf("screen height jumped across header states: %v", heights)
		}
	}
}
