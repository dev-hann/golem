package tui

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/x/ansi"

	"github.com/dev-hann/golem/pkg/golemclient"
)

// fakeClient scripts inbound events and records outbound ones.
type fakeClient struct {
	sent    []any
	inbound chan any
	closed  bool
	sendErr error
}

func newFakeClient() *fakeClient {
	return &fakeClient{inbound: make(chan any, 64)}
}

func (f *fakeClient) Send(event any) error {
	if f.sendErr != nil {
		return f.sendErr
	}
	f.sent = append(f.sent, event)
	return nil
}

func (f *fakeClient) Receive() (any, error) {
	ev, ok := <-f.inbound
	if !ok {
		return nil, errors.New("connection closed")
	}
	return ev, nil
}

func (f *fakeClient) Close() error { f.closed = true; return nil }

// wired builds a connected model over a fake client. Returned cmds are not
// executed: listen() would block on the fake Receive.
func wired(t *testing.T) (Model, *fakeClient) {
	t.Helper()
	fc := newFakeClient()
	m := New("/dev/null")
	m.dial = func(string) (client, error) { return fc, nil }
	next, _ := m.Update(connectedMsg{conn: fc})
	return next.(Model), fc
}

// key drives one key press, running the resulting cmd (safe: send only).
func key(t *testing.T, m Model, k tea.KeyMsg) Model {
	t.Helper()
	next, cmd := m.Update(k)
	if cmd != nil {
		_ = cmd()
	}
	return next.(Model)
}

// deliver applies one server event; the follow-up listen cmd is not run.
func deliver(t *testing.T, m Model, ev any) Model {
	t.Helper()
	next, _ := m.Update(serverEventMsg{ev: ev})
	return next.(Model)
}

// lastSent returns the most recently sent event.
func lastSent(t *testing.T, fc *fakeClient) any {
	t.Helper()
	if len(fc.sent) == 0 {
		t.Fatal("no events sent")
	}
	return fc.sent[len(fc.sent)-1]
}

// resize drives a window size message.
func resize(m Model, w, h int) Model {
	next, _ := m.Update(tea.WindowSizeMsg{Width: w, Height: h})
	return next.(Model)
}

// arm presses the leader key without running its tick cmd — executing it
// would stall the test for the full timeout window.
func arm(m Model) Model {
	next, _ := m.Update(tea.KeyMsg{Type: tea.KeyCtrlX})
	return next.(Model)
}

// tick delivers the leader timeout for the given arm generation.
func tick(m Model, gen int) Model {
	next, _ := m.Update(leaderTimeoutMsg{gen: gen})
	return next.(Model)
}

// rkey presses a single printable key.
func rkey(s rune) tea.KeyMsg {
	return tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{s}}
}

func TestModel_AppliesTextDelta_UpdatesView(t *testing.T) {
	t.Parallel()
	m, _ := wired(t)
	m = resize(m, 100, 40)
	m = deliver(t, m, golemclient.TurnStart{Type: golemclient.TypeTurnStart, Turn: "t1"})
	m = deliver(t, m, golemclient.TextDelta{Type: golemclient.TypeTextDelta, Turn: "t1", Delta: "Hello "})
	m = deliver(t, m, golemclient.TextDelta{Type: golemclient.TypeTextDelta, Turn: "t1", Delta: "world"})
	if got := m.View(); !strings.Contains(got, "Hello world") {
		t.Errorf("view missing streamed text:\n%s", got)
	}
	if !m.turnActive {
		t.Error("turn should be active after turn_start")
	}
	m = deliver(t, m, golemclient.TurnEnd{Type: golemclient.TypeTurnEnd, Turn: "t1"})
	if m.turnActive {
		t.Error("turn should end on turn_end")
	}
}

func TestModel_ToolCallCollapsedByDefault(t *testing.T) {
	t.Parallel()
	m, _ := wired(t)
	m = resize(m, 100, 40)

	// Running: just the summary line.
	m = deliver(t, m, golemclient.ToolCallStart{
		Type: golemclient.TypeToolCallStart, CallID: "c1", Tool: "read",
		Args: []byte(`{"path":"main.go"}`),
	})

	running := m.View()
	if !strings.Contains(running, "read main.go") {
		t.Errorf("running summary must show the parsed argument:\n%s", running)
	}

	if strings.Contains(running, "package main") {
		t.Errorf("result must stay hidden while running:\n%s", running)
	}

	m = deliver(t, m, golemclient.ToolCallEnd{
		Type: golemclient.TypeToolCallEnd, CallID: "c1", OK: true,
		Result: "1: package main\n2: func main() {}",
	})

	done := m.View()
	if !strings.Contains(done, "✓") {
		t.Errorf("summary missing success marker:\n%s", done)
	}

	if strings.Contains(done, "package main") {
		t.Errorf("successful results stay collapsed until clicked:\n%s", done)
	}

	// Failures collapse too — the ✗ marker identifies them.
	m = deliver(t, m, golemclient.ToolCallStart{
		Type: golemclient.TypeToolCallStart, CallID: "c2", Tool: "bash",
		Args: []byte(`{"command":"rm -rf /"}`),
	})
	m = deliver(t, m, golemclient.ToolCallEnd{
		Type: golemclient.TypeToolCallEnd, CallID: "c2", OK: false,
		Result: "exit 1: permission denied",
	})

	failed := m.View()
	if !strings.Contains(failed, "✗") || !strings.Contains(failed, "rm -rf /") {
		t.Errorf("failed summary must show the marker and command:\n%s", failed)
	}

	if strings.Contains(failed, "permission denied") {
		t.Errorf("failure results stay collapsed until clicked:\n%s", failed)
	}
}

func TestModel_ToolBlock_ClickTogglesResult(t *testing.T) {
	t.Parallel()

	m, _ := wired(t)
	m = resize(m, 100, 40)
	m = deliver(t, m, golemclient.ToolCallStart{
		Type: golemclient.TypeToolCallStart, CallID: "c1", Tool: "read",
		Args: []byte(`{"path":"main.go"}`),
	})
	m = deliver(t, m, golemclient.ToolCallEnd{
		Type: golemclient.TypeToolCallEnd, CallID: "c1", OK: true,
		Result: "1: package main",
	})

	if len(m.toolSpans) != 1 {
		t.Fatalf("toolSpans = %+v, want one entry", m.toolSpans)
	}

	click := func(y int) Model {
		next, _ := m.Update(tea.MouseMsg{Button: tea.MouseButtonLeft, Action: tea.MouseActionPress, X: 2, Y: y})
		return next.(Model)
	}

	m = click(m.toolSpans[0].start)

	if !strings.Contains(m.View(), "package main") {
		t.Errorf("click on the tool row must expand the result:\n%s", m.View())
	}

	m = click(m.toolSpans[0].start)

	if strings.Contains(m.View(), "package main") {
		t.Errorf("second click must collapse the result:\n%s", m.View())
	}
}

func TestModel_ToolBlock_ClickOutsideSpansIgnored(t *testing.T) {
	t.Parallel()

	m, _ := wired(t)
	m = resize(m, 100, 40)
	m = deliver(t, m, golemclient.ToolCallStart{
		Type: golemclient.TypeToolCallStart, CallID: "c1", Tool: "read",
		Args: []byte(`{"path":"main.go"}`),
	})
	m = deliver(t, m, golemclient.ToolCallEnd{
		Type: golemclient.TypeToolCallEnd, CallID: "c1", OK: true,
		Result: "1: package main",
	})

	span := m.toolSpans[0]

	// Clicks above and below the tool row (and on the composer area) do
	// nothing.
	for _, y := range []int{span.start - 1, span.end, m.vp.Height + 5} {
		next, _ := m.Update(tea.MouseMsg{Button: tea.MouseButtonLeft, Action: tea.MouseActionPress, X: 2, Y: y})
		m = next.(Model)

		if m.blocks[0].expanded {
			t.Errorf("click at y=%d must not expand the tool block", y)
		}
	}
}

func TestModel_ToolBlock_ClickAccountsForStickyHeader(t *testing.T) {
	t.Parallel()

	m, _ := wired(t)
	m = resize(m, 80, 24)

	// A user question, a long answer pushing it off-screen, then the tool
	// call visible at the top of the viewport with the sticky bar pinned.
	m.blocks = []block{
		{kind: blockUser, text: "fix the bug"},
		{kind: blockAssistant, text: strings.Repeat("answer line\n", 60)},
		{kind: blockTool, callID: "c1", tool: "read", args: `{"path":"main.go"}`, done: true, ok: true, result: "1: package main"},
		{kind: blockAssistant, text: strings.Repeat("tail line\n", 40)},
	}
	m.refreshView()

	// Scroll so the tool row is the first content line and the question
	// scrolls off — the sticky bar pins above the viewport.
	span := m.toolSpans[0]
	m.vp.YOffset = span.start
	m.refreshView()

	if m.stickyText() != "fix the bug" {
		t.Fatalf("precondition: sticky header must be visible, got %q", m.stickyText())
	}

	// Screen row = sticky line (1) + content line - YOffset.
	next, _ := m.Update(tea.MouseMsg{
		Button: tea.MouseButtonLeft, Action: tea.MouseActionPress, X: 2, Y: 1,
	})
	m = next.(Model)

	if !m.blocks[2].expanded {
		t.Error("click through the sticky offset must hit the tool row")
	}
}

func TestModel_FooterGone(t *testing.T) {
	t.Parallel()
	m, _ := wired(t)
	m = resize(m, 80, 24)
	m = deliver(t, m, golemclient.TurnStart{
		Type: golemclient.TypeTurnStart, Provider: "zai-coding", Model: "glm-5.3",
	})
	m = deliver(t, m, golemclient.TextDelta{Type: golemclient.TypeTextDelta, Delta: "hi"})
	m = deliver(t, m, golemclient.TurnEnd{Type: golemclient.TypeTurnEnd})

	view := m.View()
	for _, stale := range []string{"thinking…", "done (in=", "zai-coding"} {
		if strings.Contains(view, stale) {
			t.Errorf("status footer should be gone, found %q:\n%s", stale, view)
		}
	}
}

func TestModel_InterruptSendsInterruptEvent_ThenQuitsWhenIdle(t *testing.T) {
	t.Parallel()
	m, fc := wired(t)
	m = deliver(t, m, golemclient.TurnStart{Type: golemclient.TypeTurnStart})
	m = key(t, m, tea.KeyMsg{Type: tea.KeyCtrlC})
	if _, ok := lastSent(t, fc).(golemclient.Interrupt); !ok {
		t.Fatalf("ctrl+c during turn should interrupt, sent=%v", fc.sent)
	}
	next, cmd := m.Update(tea.KeyMsg{Type: tea.KeyCtrlC})
	if cmd == nil {
		t.Error("idle ctrl+c should quit")
	}
	_ = next
}

func TestModel_SubmitSendsUserMessage(t *testing.T) {
	t.Parallel()
	m, fc := wired(t)
	m = deliver(t, m, golemclient.SessionMeta{Type: golemclient.TypeSessionMeta, Session: "s1"})
	var cmd tea.Cmd
	for _, r := range "fix the bug" {
		var next tea.Model
		next, cmd = m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{r}})
		m = next.(Model)
	}
	_ = cmd
	next2, cmd2 := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	if cmd2 != nil {
		_ = cmd2()
	}
	m = next2.(Model)
	msg, ok := lastSent(t, fc).(golemclient.UserMessage)
	if !ok || msg.Text != "fix the bug" || msg.Session != "s1" {
		t.Errorf("sent = %#v, want user_message 'fix the bug' for s1", msg)
	}
	if m.input.Value() != "" {
		t.Error("input should clear after submit")
	}
}

func TestModel_SessionPickerListsAndSwitches(t *testing.T) {
	t.Parallel()
	m, fc := wired(t)
	m = resize(m, 100, 40)
	m = deliver(t, m, golemclient.SessionMeta{Type: golemclient.TypeSessionMeta, Session: "s1"})
	m.listRequest = true
	m = deliver(t, m, golemclient.SessionList{
		Type: golemclient.TypeSessionList,
		Sessions: []golemclient.SessionSummary{
			{ID: "old", Cwd: "/a", Turns: 1},
			{ID: "new", Cwd: "/b", Turns: 2},
		},
	})
	if m.mode != modeSessions {
		t.Fatalf("mode = %v, want sessions picker", m.mode)
	}
	if !strings.Contains(m.View(), "▶ a") {
		t.Errorf("picker rows missing:\n%s", m.View())
	}
	m = key(t, m, tea.KeyMsg{Type: tea.KeyDown})
	m = key(t, m, tea.KeyMsg{Type: tea.KeyEnter})
	loaded, ok := lastSent(t, fc).(golemclient.LoadSession)
	if !ok || loaded.Session != "new" {
		t.Errorf("load = %#v, want session new", loaded)
	}
	if m.mode != modeChat {
		t.Errorf("mode after pick = %v, want chat", m.mode)
	}
}

func TestModel_SessionPickerEscapeCancels(t *testing.T) {
	t.Parallel()
	m, _ := wired(t)
	m.listRequest = true
	m = deliver(t, m, golemclient.SessionList{
		Type:     golemclient.TypeSessionList,
		Sessions: []golemclient.SessionSummary{{ID: "s", Cwd: "/a"}},
	})
	m = key(t, m, tea.KeyMsg{Type: tea.KeyEsc})
	if m.mode != modeChat {
		t.Errorf("esc should cancel picker, mode = %v", m.mode)
	}
}

func TestModel_SessionPickerShowsTitleAndRelativeTime(t *testing.T) {
	t.Parallel()

	m, _ := wired(t)
	m = resize(m, 100, 40)
	twoHoursAgo := time.Now().Add(-2 * time.Hour).UTC().Format(time.RFC3339)
	m.listRequest = true
	m = deliver(t, m, golemclient.SessionList{
		Type: golemclient.TypeSessionList,
		Sessions: []golemclient.SessionSummary{{
			ID: "01JTESTSESSIONIDXXXXXXXXXX", Title: "Fix auth middleware",
			Cwd: "/w/golem", Turns: 3, LastActive: twoHoursAgo,
		}},
	})

	view := m.View()
	if !strings.Contains(view, "Fix auth middleware · 2h ago") {
		t.Errorf("picker row should show title and date only:\n%s", view)
	}

	if strings.Contains(view, "golem · 3 turns") {
		t.Errorf("dir and turns are noise on the row:\n%s", view)
	}

	if strings.Contains(view, "01JTESTSESSIONID") {
		t.Errorf("raw session id must not render when a title exists:\n%s", view)
	}
}

// TestModel_SessionPickerMarksCurrentSession verifies the picker tags the
// row of the session this client is in with · current and prepositions
// the cursor on it.
func TestModel_SessionPickerMarksCurrentSession(t *testing.T) {
	t.Parallel()

	m, _ := wired(t)
	m = resize(m, 100, 40)
	m = deliver(t, m, golemclient.SessionMeta{Type: golemclient.TypeSessionMeta, Session: "s1"})
	m.listRequest = true
	m = deliver(t, m, golemclient.SessionList{
		Type: golemclient.TypeSessionList,
		Sessions: []golemclient.SessionSummary{
			{ID: "s1", Title: "This one", Cwd: "/a", Turns: 1},
			{ID: "s2", Title: "Another", Cwd: "/b", Turns: 2},
		},
	})

	view := m.View()
	if !strings.Contains(view, "This one · current") {
		t.Errorf("picker must mark the client's session as current:\n%s", view)
	}
	if strings.Contains(view, "Another · current") {
		t.Errorf("only the client's session may carry the current marker:\n%s", view)
	}
	if m.pickIndex != 0 {
		t.Errorf("pickIndex = %d, want 0 (cursor starts on the current session)", m.pickIndex)
	}
	if !strings.Contains(view, "▶ This one") {
		t.Errorf("cursor must sit on the current session row:\n%s", view)
	}
}

// TestModel_SessionPickerShowsRunningStatus verifies the picker renders
// · running for sessions the server reports busy (the flag arrives on the
// wire) and reflects a locally active turn even when the flag is stale.
func TestModel_SessionPickerShowsRunningStatus(t *testing.T) {
	t.Parallel()

	m, _ := wired(t)
	m = resize(m, 100, 40)
	m = deliver(t, m, golemclient.SessionMeta{Type: golemclient.TypeSessionMeta, Session: "s1"})
	m.listRequest = true
	m = deliver(t, m, golemclient.SessionList{
		Type: golemclient.TypeSessionList,
		Sessions: []golemclient.SessionSummary{
			{ID: "s1", Title: "Mine", Cwd: "/a", Turns: 1},
			{ID: "s2", Title: "Elsewhere", Cwd: "/b", Turns: 2, Busy: true},
		},
	})

	view := m.View()
	if !strings.Contains(view, "Elsewhere · running") {
		t.Errorf("server-reported busy session must show · running:\n%s", view)
	}

	// A local turn active in this client's session shows running even
	// when the list snapshot predates the turn.
	m = deliver(t, m, golemclient.TurnStart{
		Type: golemclient.TypeTurnStart, Session: "s1", Turn: "t1",
		Provider: "zai", Model: "glm-5.3",
	})
	if !strings.Contains(m.View(), "Mine · current · running") {
		t.Errorf("locally active session must show · running:\n%s", m.View())
	}

	// Turn end clears it.
	m = deliver(t, m, golemclient.TurnEnd{Type: golemclient.TypeTurnEnd, Session: "s1", Turn: "t1"})
	if strings.Contains(m.View(), "Mine · current · running") {
		t.Errorf("turn end must clear the local running marker:\n%s", m.View())
	}
}

// TestModel_SessionPickerLocalTurnOverridesStaleBusyFlag verifies that
// for the session this client is in, the local turn state wins over the
// busy flag snapshotted at list time: an idle local turn clears a stale
// server busy marker.
func TestModel_SessionPickerLocalTurnOverridesStaleBusyFlag(t *testing.T) {
	t.Parallel()

	m, _ := wired(t)
	m = resize(m, 100, 40)
	m = deliver(t, m, golemclient.SessionMeta{Type: golemclient.TypeSessionMeta, Session: "s1"})
	m.listRequest = true
	m = deliver(t, m, golemclient.SessionList{
		Type:     golemclient.TypeSessionList,
		Sessions: []golemclient.SessionSummary{{ID: "s1", Title: "Mine", Cwd: "/a", Busy: true}},
	})

	if strings.Contains(m.View(), "running") {
		t.Errorf("stale busy flag must not mark the idle local session running:\n%s", m.View())
	}
}

func TestModel_SessionPickerSearchFilters(t *testing.T) {
	t.Parallel()

	m, fc := wired(t)
	m = resize(m, 100, 40)
	m.listRequest = true
	m = deliver(t, m, golemclient.SessionList{
		Type: golemclient.TypeSessionList,
		Sessions: []golemclient.SessionSummary{
			{ID: "s1", Title: "Fix auth middleware", Cwd: "/a"},
			{ID: "s2", Title: "Add session tests", Cwd: "/b"},
			{ID: "s3", Title: "Refactor db layer", Cwd: "/c"},
		},
	})

	for _, r := range "fix" {
		m = key(t, m, rkey(r))
	}

	view := m.View()
	if !strings.Contains(view, "Fix auth middleware") {
		t.Errorf("filter must keep the matching row:\n%s", view)
	}

	for _, gone := range []string{"Add session tests", "Refactor db layer"} {
		if strings.Contains(view, gone) {
			t.Errorf("filter must drop %q:\n%s", gone, view)
		}
	}

	m = key(t, m, tea.KeyMsg{Type: tea.KeyEnter})

	loaded, ok := lastSent(t, fc).(golemclient.LoadSession)
	if !ok || loaded.Session != "s1" {
		t.Errorf("load = %#v, want s1 from the filtered list", loaded)
	}
}

func TestModel_SessionPickerRenameFlow(t *testing.T) {
	t.Parallel()

	m, fc := wired(t)
	m = resize(m, 100, 40)
	m.listRequest = true
	m = deliver(t, m, golemclient.SessionList{
		Type: golemclient.TypeSessionList,
		Sessions: []golemclient.SessionSummary{
			{ID: "s1", Title: "Fix auth middleware", Cwd: "/a"},
		},
	})

	m = key(t, m, tea.KeyMsg{Type: tea.KeyCtrlR})
	if !m.sessRenaming {
		t.Fatalf("ctrl+r must start renaming")
	}

	if got := m.sessRenameText; got != "Fix auth middleware" {
		t.Errorf("rename input = %q, want the current title seeded", got)
	}

	m = key(t, m, tea.KeyMsg{Type: tea.KeyCtrlU}) // clear the seeded text
	for _, r := range "New name" {
		m = key(t, m, rkey(r))
	}

	m = key(t, m, tea.KeyMsg{Type: tea.KeyEnter})

	sent, ok := lastSent(t, fc).(golemclient.RenameSession)
	if !ok || sent.Session != "s1" || sent.Title != "New name" {
		t.Fatalf("sent = %#v, want rename s1 to 'New name'", sent)
	}

	if m.sessRenaming {
		t.Error("enter must close the rename input")
	}

	if m.sessions[0].Title != "New name" {
		t.Errorf("local row title = %q, want the optimistic rename", m.sessions[0].Title)
	}
}

func TestModel_SessionPickerRenameEscapeCancels(t *testing.T) {
	t.Parallel()

	m, fc := wired(t)
	m = resize(m, 100, 40)
	m.listRequest = true
	m = deliver(t, m, golemclient.SessionList{
		Type:     golemclient.TypeSessionList,
		Sessions: []golemclient.SessionSummary{{ID: "s1", Title: "Old", Cwd: "/a"}},
	})

	m = key(t, m, tea.KeyMsg{Type: tea.KeyCtrlR})
	m = key(t, m, tea.KeyMsg{Type: tea.KeyEscape})

	if m.sessRenaming || m.sessRenameText != "" {
		t.Errorf("esc must cancel renaming: renaming=%v text=%q", m.sessRenaming, m.sessRenameText)
	}

	if len(fc.sent) != 0 {
		t.Errorf("canceled rename sent events: %+v", fc.sent)
	}
}

func TestModel_SessionPickerEscClearsQueryThenCloses(t *testing.T) {
	t.Parallel()

	m, _ := wired(t)
	m = resize(m, 100, 40)
	m.listRequest = true
	m = deliver(t, m, golemclient.SessionList{
		Type:     golemclient.TypeSessionList,
		Sessions: []golemclient.SessionSummary{{ID: "s1", Title: "Fix auth", Cwd: "/a"}},
	})

	m = key(t, m, rkey('x'))

	if m.sessQuery != "x" {
		t.Fatalf("query = %q, want x", m.sessQuery)
	}

	m = key(t, m, tea.KeyMsg{Type: tea.KeyEscape})

	if m.mode != modeSessions || m.sessQuery != "" {
		t.Fatalf("first esc must clear the query, mode=%v query=%q", m.mode, m.sessQuery)
	}

	m = key(t, m, tea.KeyMsg{Type: tea.KeyEscape})

	if m.mode != modeChat {
		t.Fatalf("second esc must close the picker, mode=%v", m.mode)
	}
}

func TestModel_SessionTitleEventUpdatesListRow(t *testing.T) {
	t.Parallel()

	m, _ := wired(t)
	m = resize(m, 100, 40)
	m.listRequest = true
	m = deliver(t, m, golemclient.SessionList{
		Type:     golemclient.TypeSessionList,
		Sessions: []golemclient.SessionSummary{{ID: "s1", Title: "Old title", Cwd: "/a"}},
	})

	m = deliver(t, m, golemclient.SessionTitle{
		Type: golemclient.TypeSessionTitle, Session: "s1", Title: "Server title",
	})

	if !strings.Contains(m.View(), "Server title") {
		t.Errorf("session_title event must refresh the open list:\n%s", m.View())
	}
}

func TestModel_HistoryReplayRendersTranscript(t *testing.T) {
	t.Parallel()
	m, _ := wired(t)
	m = resize(m, 100, 40)
	lines := []string{
		`{"type":"user_message","session":"s1","text":"read go.mod"}`,
		`{"type":"text_delta","session":"s1","delta":"module golem"}`,
		`{"type":"tool_call_start","session":"s1","call_id":"c1","tool":"read","args":{"path":"go.mod"}}`,
		`{"type":"tool_call_end","session":"s1","call_id":"c1","ok":true,"result":"module golem"}`,
	}
	events := make([]json.RawMessage, 0, len(lines))
	for _, line := range lines {
		events = append(events, json.RawMessage(line))
	}
	m = deliver(t, m, golemclient.History{
		Type: golemclient.TypeHistory, Session: "s1", Events: events,
	})
	view := m.View()
	for _, want := range []string{"read go.mod", "module golem", "read"} {
		if !strings.Contains(view, want) {
			t.Errorf("replayed transcript missing %q:\n%s", want, view)
		}
	}
}

func TestModel_HistoryReplayOpensAtBottom(t *testing.T) {
	t.Parallel()
	m, _ := wired(t)
	m = resize(m, 80, 24)

	// Long transcript (well over the viewport height), bookended by
	// markers only visible from one end.
	const deltaLines = 300
	raw := make([]json.RawMessage, 0, deltaLines+3)
	raw = append(raw,
		json.RawMessage(`{"type":"user_message","session":"s1","text":"old question"}`),
		json.RawMessage(`{"type":"text_delta","session":"s1","delta":"head-marker\n"}`),
	)
	for range deltaLines {
		raw = append(raw, json.RawMessage(
			`{"type":"text_delta","session":"s1","delta":"`+strings.Repeat("x", 60)+`\n"}`,
		))
	}
	raw = append(raw, json.RawMessage(
		`{"type":"text_delta","session":"s1","delta":"tail-marker\n"}`,
	))

	m = deliver(t, m, golemclient.History{Type: golemclient.TypeHistory, Session: "s1", Events: raw})

	if !m.follow {
		t.Error("history replay should restore auto-follow (open at the bottom)")
	}

	view := m.View()
	if !strings.Contains(view, "tail-marker") {
		t.Errorf("replayed transcript should open showing its tail:\n%s", view)
	}
	// Content cannot distinguish top from bottom here (the x block fills
	// both ends and the sticky header pins the opening question by
	// design), so assert the scroll position itself.
	if !m.vp.AtBottom() {
		t.Errorf("history replay should open at the bottom: YOffset=%d height=%d",
			m.vp.YOffset, m.vp.Height)
	}
}

func TestViewport_HandlesLongOutputScrollback(t *testing.T) {
	t.Parallel()
	m, _ := wired(t)
	m = resize(m, 80, 24)
	m = deliver(t, m, golemclient.TurnStart{Type: golemclient.TypeTurnStart})
	m = deliver(t, m, golemclient.TextDelta{
		Type: golemclient.TypeTextDelta, Delta: "first-line-marker\n",
	})
	for range 500 {
		// 500 deltas exceed any viewport height; content must be paged,
		// not dropped.
		m = deliver(t, m, golemclient.TextDelta{
			Type:  golemclient.TypeTextDelta,
			Delta: strings.Repeat("x", 60) + "\n",
		})
	}
	m = deliver(t, m, golemclient.TextDelta{
		Type: golemclient.TypeTextDelta, Delta: "last-line-marker",
	})
	view := m.View()
	if !strings.Contains(view, "last-line-marker") {
		t.Error("viewport should auto-scroll to the streaming tail")
	}
	if strings.Contains(view, "first-line-marker") {
		t.Error("off-screen head should not render in the visible page")
	}
}

func TestModel_StickyUserPinsWhenScrolledPast(t *testing.T) {
	t.Parallel()
	m, _ := wired(t)
	m = resize(m, 80, 24)
	m.blocks = []block{{kind: blockUser, text: "fix the bug"}}
	m.refreshView()
	view := m.View()
	if !strings.Contains(view, "▎ fix the bug") {
		t.Fatalf("user bar should be visible before scrolling:\n%s", view)
	}
	if m.stickyText() != "" {
		t.Errorf("sticky must stay hidden while the bar is visible")
	}

	m = deliver(t, m, golemclient.TurnStart{Type: golemclient.TypeTurnStart})
	for range 100 {
		m = deliver(t, m, golemclient.TextDelta{
			Type: golemclient.TypeTextDelta, Delta: strings.Repeat("x", 60) + "\n",
		})
	}
	view = m.View()
	if m.stickyText() != "fix the bug" {
		t.Errorf("sticky should pin the last question above the viewport")
	}
	if !strings.Contains(view, "▎ fix the bug") {
		t.Errorf("sticky line should render in the layout:\n%s", view)
	}
	// The original bar line scrolled off: only the sticky copy renders.
	if n := strings.Count(view, "▎ fix the bug"); n != 1 {
		t.Errorf("scrolled-off bar should not render in the visible page, found %d:\n%s", n, view)
	}
}

func TestModel_StickyUserClearsWithTranscript(t *testing.T) {
	t.Parallel()
	m, _ := wired(t)
	m = resize(m, 80, 24)
	m.blocks = []block{{kind: blockUser, text: "fix the bug"}}
	m.refreshView()
	m.blocks = nil
	m.refreshView()
	if strings.Contains(m.View(), "▎") {
		t.Errorf("sticky should disappear when the transcript clears:\n%s", m.View())
	}
}

func TestModel_HeaderAndSessionStatusGone(t *testing.T) {
	t.Parallel()
	m, _ := wired(t)
	m = resize(m, 80, 24)
	m = deliver(t, m, golemclient.SessionMeta{
		Type: golemclient.TypeSessionMeta, Session: "sess-abcd1234", Turns: 3,
	})
	view := m.View()
	if strings.Contains(view, "golem —") {
		t.Errorf("header line should be removed:\n%s", view)
	}
	if strings.Contains(view, "session sess-abcd1234") || strings.Contains(view, "turns") {
		t.Errorf("session status parts should be removed from the status bar:\n%s", view)
	}
}

func TestModel_ManualScrollSuspendsAutoFollow(t *testing.T) {
	t.Parallel()
	m, _ := wired(t)
	m = resize(m, 80, 24)
	m = deliver(t, m, golemclient.TurnStart{Type: golemclient.TypeTurnStart})
	m = deliver(t, m, golemclient.TextDelta{Type: golemclient.TypeTextDelta, Delta: "head-marker\n"})
	for range 100 {
		m = deliver(t, m, golemclient.TextDelta{
			Type: golemclient.TypeTextDelta, Delta: strings.Repeat("x", 60) + "\n",
		})
	}

	// Scroll back mid-stream: the next delta must not snap to the bottom.
	for range 20 {
		m = key(t, m, tea.KeyMsg{Type: tea.KeyPgUp})
	}
	m = deliver(t, m, golemclient.TextDelta{Type: golemclient.TypeTextDelta, Delta: "mid\n"})
	view := m.View()
	if !strings.Contains(view, "head-marker") {
		t.Errorf("manual scrollback should survive streaming deltas:\n%s", view)
	}
	if strings.Contains(view, "mid") {
		t.Errorf("tail should stay off-screen while scrolled back:\n%s", view)
	}

	// Paging back to the bottom resumes the follow.
	for range 20 {
		m = key(t, m, tea.KeyMsg{Type: tea.KeyPgDown})
	}

	m = deliver(t, m, golemclient.TextDelta{Type: golemclient.TypeTextDelta, Delta: "tail-marker"})
	if !strings.Contains(m.View(), "tail-marker") {
		t.Errorf("reaching the bottom should resume auto-follow:\n%s", m.View())
	}
}

func TestModel_MouseWheelScrollsTranscript(t *testing.T) {
	t.Parallel()
	m, _ := wired(t)
	m = resize(m, 80, 24)
	m = deliver(t, m, golemclient.TurnStart{Type: golemclient.TypeTurnStart})
	m = deliver(t, m, golemclient.TextDelta{Type: golemclient.TypeTextDelta, Delta: "head-marker\n"})
	for range 100 {
		m = deliver(t, m, golemclient.TextDelta{
			Type: golemclient.TypeTextDelta, Delta: strings.Repeat("x", 60) + "\n",
		})
	}

	// Wheel back: the next delta must not snap to the bottom.
	for range 40 {
		next, _ := m.Update(tea.MouseMsg{Button: tea.MouseButtonWheelUp})
		m = next.(Model)
	}

	m = deliver(t, m, golemclient.TextDelta{Type: golemclient.TypeTextDelta, Delta: "mid\n"})
	view := m.View()
	if !strings.Contains(view, "head-marker") {
		t.Errorf("wheel scrollback should survive streaming deltas:\n%s", view)
	}
	if strings.Contains(view, "mid") {
		t.Errorf("tail should stay off-screen while wheeled back:\n%s", view)
	}

	// Wheel to the bottom resumes the follow.
	for range 40 {
		next, _ := m.Update(tea.MouseMsg{Button: tea.MouseButtonWheelDown})
		m = next.(Model)
	}

	m = deliver(t, m, golemclient.TextDelta{Type: golemclient.TypeTextDelta, Delta: "tail-marker"})
	if !strings.Contains(m.View(), "tail-marker") {
		t.Errorf("reaching the bottom should resume auto-follow:\n%s", m.View())
	}
}

func TestModel_StickyTracksScrollRegion(t *testing.T) {
	t.Parallel()
	m, _ := wired(t)
	m = resize(m, 80, 24)
	answer := strings.Repeat("answer line\n", 200)
	m.blocks = []block{
		{kind: blockUser, text: "first question"},
		{kind: blockAssistant, text: answer},
		{kind: blockUser, text: "second question"},
		{kind: blockAssistant, text: answer},
	}
	m.refreshView()

	// Reading the first answer's middle: the sticky names its question.
	m.vp.YOffset = 100
	if got := m.stickyText(); got != "first question" {
		t.Errorf("sticky in first-answer region = %q, want first question", got)
	}

	// Reading the second answer: the sticky switches questions.
	m.vp.YOffset = 300
	if got := m.stickyText(); got != "second question" {
		t.Errorf("sticky in second-answer region = %q, want second question", got)
	}

	// A question fully visible in the viewport needs no sticky.
	m.vp.YOffset = 0
	if got := m.stickyText(); got != "" {
		t.Errorf("sticky with the question visible = %q, want none", got)
	}
}

func TestModel_ConnectFailureShowsError(t *testing.T) {
	t.Parallel()
	m := New("/dev/null")
	m.dial = func(string) (client, error) {
		return nil, errors.New("no server at /dev/null")
	}
	cmd := m.Init()
	msg := cmd()
	connErr, ok := msg.(connErrMsg)
	if !ok || !strings.Contains(connErr.err.Error(), "no server") {
		t.Fatalf("Init cmd = %#v, want connect error", msg)
	}
	next, _ := m.Update(connErr)
	view := next.(Model).View()
	if !strings.Contains(view, "no server") {
		t.Errorf("view should show connect error:\n%s", view)
	}
}

func TestArgSummary_ParsesToolSpecificFields(t *testing.T) {
	t.Parallel()

	cases := []struct {
		tool string
		args string
		want string
	}{
		{"read", `{"path":"internal/tui/model.go"}`, "internal/tui/model.go"},
		{"edit", `{"path":"main.go","old":"a","new":"b"}`, "main.go"},
		{"write", `{"path":"out.txt","content":"…"}`, "out.txt"},
		{"bash", `{"command":"npm test 2>&1"}`, "npm test 2>&1"},
		{"grep", `{"pattern":"func Test","path":"."}`, `func Test .`},
		{"glob", `{"pattern":"**/*.go"}`, "**/*.go"},
		{"unknown", `{"x":1,"y":2}`, `{"x":1,"y":2}`},
		{"read", `{not json}`, "{not json}"},
	}

	for _, tc := range cases {
		t.Run(tc.tool+"/"+tc.args, func(t *testing.T) {
			t.Parallel()

			if got := argSummary(tc.tool, tc.args); got != tc.want {
				t.Errorf("argSummary(%s, %s) = %q, want %q", tc.tool, tc.args, got, tc.want)
			}
		})
	}
}

func TestToolBlock_ClickParityWithRenderedRows(t *testing.T) {
	t.Parallel()

	build := func() Model {
		m, _ := wired(t)
		m = resize(m, 80, 30)
		m.blocks = []block{
			{kind: blockUser, text: "first question"},
			{kind: blockTool, tool: "read", args: `{"path":"main.go"}`, done: true, ok: true, result: "1: package main"},
			{kind: blockAssistant, text: "answer one\nanswer two\nanswer three"},
			{kind: blockTool, tool: "bash", args: `{"command":"npm test"}`, done: true, ok: true, result: "ok"},
			{kind: blockTool, tool: "grep", args: `{"pattern":"foo","path":"."}`, done: true, ok: false, result: "no match"},
			{kind: blockUser, text: "second question"},
			{kind: blockAssistant, text: strings.Repeat("tail answer\n", 40)},
		}
		m.refreshView()

		return m
	}

	topRowOf := func(m Model) int {
		if m.stickyText() != "" {
			return 1
		}

		return 0
	}

	// Screen offsets: top, middle, and deep enough to pin the sticky bar.
	for _, yOff := range []int{0, 3, 8} {
		for _, span := range build().toolSpans {
			m := build()
			m.vp.YOffset = yOff
			m.refreshView()

			topRow := topRowOf(m)
			screenY := topRow + span.start - m.vp.YOffset

			if screenY-topRow < 0 || screenY-topRow >= m.vp.Height {
				continue // row scrolled out of the viewport
			}

			// The pre-click view: every tool row collapsed.
			viewLines := strings.Split(m.View(), "\n")
			if screenY >= len(viewLines) {
				t.Fatalf("yOffset=%d: screen row %d beyond view (%d lines)", yOff, screenY, len(viewLines))
			}

			rowText := viewLines[screenY]

			// Snapshot expansion state: Model copies share the blocks
			// backing array, so post-click comparison needs a copy.
			before := make([]bool, len(m.blocks))

			for i := range m.blocks {
				before[i] = m.blocks[i].expanded
			}

			next, _ := m.Update(tea.MouseMsg{
				Button: tea.MouseButtonLeft, Action: tea.MouseActionPress, X: 0, Y: screenY,
			})
			after := next.(Model)

			toggled := -1

			for i := range after.blocks {
				if after.blocks[i].expanded != before[i] {
					toggled = i
				}
			}

			if toggled < 0 {
				t.Fatalf("yOffset=%d span%+v screenY=%d: click toggled nothing (row=%q)",
					yOff, span, screenY, rowText)
			}

			want := argSummary(after.blocks[toggled].tool, after.blocks[toggled].args)

			if !strings.Contains(rowText, want) {
				t.Errorf("yOffset=%d span%+v screenY=%d toggled block %d (%q) but the rendered row is %q",
					yOff, span, screenY, toggled, want, rowText)
			}

			// Shared backing array: undo the toggle before the next probe.
			m.blocks[toggled].expanded = false
			m.refreshView()
		}
	}
}

func TestLayout_HeightConstantWithAndWithoutStatus(t *testing.T) {
	t.Parallel()
	m, _ := wired(t)
	m = resize(m, 80, 24)

	idle := strings.Count(m.View(), "\n") + 1

	m = deliver(t, m, golemclient.TurnStart{Type: golemclient.TypeTurnStart})
	m = deliver(t, m, golemclient.ToolCallStart{
		Type: golemclient.TypeToolCallStart, CallID: "c1", Tool: "bash",
		Args: []byte(`{"command":"go test ./..."}`),
	})

	if active := strings.Count(m.View(), "\n") + 1; active != idle {
		t.Errorf("layout height changed: idle=%d active=%d", idle, active)
	}

	m = deliver(t, m, golemclient.TurnEnd{Type: golemclient.TypeTurnEnd})
	m = deliver(t, m, golemclient.UserMessage{Type: golemclient.TypeUserMessage, Text: "long question over many lines"})

	if scrolled := strings.Count(m.View(), "\n") + 1; scrolled != idle {
		t.Errorf("layout height changed with sticky header: idle=%d sticky=%d", idle, scrolled)
	}
}

func TestModalViews_FitTerminalWidth(t *testing.T) {
	t.Parallel()

	long := strings.Repeat("session-title-", 8)

	tests := []struct {
		name  string
		setup func(t *testing.T) Model
		width int
	}{
		{
			name: "command palette with long query",
			setup: func(t *testing.T) Model {
				t.Helper()

				m, _ := wired(t)
				m = resize(m, 40, 20)
				m = key(t, m, tea.KeyMsg{Type: tea.KeyCtrlP})
				m = key(t, m, rkey('n'))
				m = key(t, m, rkey('e'))
				m = key(t, m, rkey('w'))

				return m
			},
			width: 40,
		},
		{
			name: "model picker with long ids",
			setup: func(t *testing.T) Model {
				t.Helper()

				m, _ := wired(t)
				m = resize(m, 36, 20)
				m = m.WithModels([]string{
					"provider/" + strings.Repeat("long-model-name/", 4),
					"short",
				})
				m = deliver(t, m, golemclient.TurnStart{
					Type: golemclient.TypeTurnStart, Provider: "p", Model: "short",
				})
				m = arm(m)
				m = key(t, m, rkey('m'))

				return m
			},
			width: 36,
		},
		{
			name: "session picker with long titles and rename input",
			setup: func(t *testing.T) Model {
				t.Helper()

				m, _ := wired(t)
				m = resize(m, 38, 24)
				m = deliver(t, m, golemclient.SessionMeta{Type: golemclient.TypeSessionMeta, Session: "s1"})
				m = arm(m)
				m = key(t, m, rkey('l'))
				m = deliver(t, m, golemclient.SessionList{
					Type: golemclient.TypeSessionList,
					Sessions: []golemclient.SessionSummary{
						{ID: "s1", Title: long, Cwd: "/work/very/deeply/nested/project/directory"},
						{ID: "s2", Title: long + "-second", LastActive: "2025-01-01T00:00:00Z"},
					},
				})
				m = key(t, m, tea.KeyMsg{Type: tea.KeyCtrlR})
				for range 30 {
					m = key(t, m, rkey('x'))
				}

				return m
			},
			width: 38,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			m := tt.setup(t)

			for i, line := range strings.Split(m.View(), "\n") {
				if w := ansi.StringWidth(line); w > tt.width {
					t.Fatalf("row %d width %d exceeds %d:\n%s", i, w, tt.width, m.View())
				}
			}
		})
	}
}
