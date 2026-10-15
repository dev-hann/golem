package tui

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/dev-hann/golem/internal/tui/kit"
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
func key(t *testing.T, m Model, k tea.KeyPressMsg) Model {
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
	next, _ := m.Update(tea.KeyPressMsg{Code: 'x', Mod: tea.ModCtrl})
	return next.(Model)
}

// tick delivers the leader timeout for the given arm generation.
func tick(m Model, gen int) Model {
	next, _ := m.Update(leaderTimeoutMsg{gen: gen})
	return next.(Model)
}

// exitTick delivers the exit-confirmation timeout for the given generation.
func exitTick(t *testing.T, m Model, gen int) Model {
	t.Helper()
	next, _ := m.Update(exitTimeoutMsg{gen: gen})
	return next.(Model)
}

// rkey presses a single printable key.
func rkey(s rune) tea.KeyPressMsg {
	return textKey(s)
}

// flattenCmds expands a (possibly batched) cmd into its leaves so tests
// can run each and pick out the message they await.
func flattenCmds(cmd tea.Cmd) []tea.Cmd {
	if cmd == nil {
		return nil
	}

	switch c := cmd().(type) {
	case tea.BatchMsg:
		var out []tea.Cmd
		for _, sub := range c {
			out = append(out, flattenCmds(sub)...)
		}

		return out
	default:
		return []tea.Cmd{cmd}
	}
}

// textKey builds a printable-key press carrying rune r.
func textKey(r rune) tea.KeyPressMsg {
	return tea.KeyPressMsg{Code: r, Text: string(r)}
}

// altEnterKey builds the alt/option+enter press that inserts a newline.
func altEnterKey() tea.KeyPressMsg {
	return tea.KeyPressMsg{Code: tea.KeyEnter, Mod: tea.ModAlt}
}

// shiftEnterKey builds the shift+enter press — distinguishable only on
// terminals with the kitty keyboard protocol; the handler binds it
// alongside alt+enter.
func shiftEnterKey() tea.KeyPressMsg {
	return tea.KeyPressMsg{Code: tea.KeyEnter, Mod: tea.ModShift}
}

func TestModel_AppliesTextDelta_UpdatesView(t *testing.T) {
	t.Parallel()
	m, _ := wired(t)
	m = resize(m, 100, 40)
	m = deliver(t, m, golemclient.TurnStart{Type: golemclient.TypeTurnStart, Turn: "t1"})
	m = deliver(t, m, golemclient.TextDelta{Type: golemclient.TypeTextDelta, Turn: "t1", Delta: "Hello "})
	m = deliver(t, m, golemclient.TextDelta{Type: golemclient.TypeTextDelta, Turn: "t1", Delta: "world"})
	if got := m.View().Content; !strings.Contains(got, "Hello world") {
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

	running := m.View().Content
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

	done := m.View().Content
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

	failed := m.View().Content
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
		next, _ := m.Update(tea.MouseClickMsg{Button: tea.MouseLeft, X: 2, Y: y})
		return next.(Model)
	}

	m = click(stickyTopRow(m) + m.toolSpans[0].start - m.vp.YOffset())

	if !strings.Contains(m.View().Content, "package main") {
		t.Errorf("click on the tool row must expand the result:\n%s", m.View().Content)
	}

	m = click(stickyTopRow(m) + m.toolSpans[0].start - m.vp.YOffset())

	if strings.Contains(m.View().Content, "package main") {
		t.Errorf("second click must collapse the result:\n%s", m.View().Content)
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
	for _, y := range []int{stickyTopRow(m) + span.start - 1, stickyTopRow(m) + span.end, m.vp.Height() + screenPadV + layoutChromeRows + 5} {
		next, _ := m.Update(tea.MouseClickMsg{Button: tea.MouseLeft, X: 2, Y: y})
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
	m.vp.SetYOffset(span.start)
	m.refreshView()

	if m.stickyText() != "fix the bug" {
		t.Fatalf("precondition: sticky header must be visible, got %q", m.stickyText())
	}

	// Screen row = sticky line (1) + content line - YOffset.
	next, _ := m.Update(tea.MouseClickMsg{Button: tea.MouseLeft, X: 2, Y: stickyTopRow(m)})
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

	view := m.View().Content
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
	m = key(t, m, tea.KeyPressMsg{Code: 'c', Mod: tea.ModCtrl})
	if _, ok := lastSent(t, fc).(golemclient.Interrupt); !ok {
		t.Fatalf("ctrl+c during turn should interrupt, sent=%v", fc.sent)
	}
	next, cmd := m.Update(tea.KeyPressMsg{Code: 'c', Mod: tea.ModCtrl})
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
		next, cmd = m.Update(textKey(r))
		m = next.(Model)
	}
	_ = cmd
	next2, cmd2 := m.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
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
	if m.pickIndex != 0 {
		t.Errorf("pickIndex = %d, want the cursor on the first row", m.pickIndex)
	}
	m = key(t, m, tea.KeyPressMsg{Code: tea.KeyDown})
	m = key(t, m, tea.KeyPressMsg{Code: tea.KeyEnter})
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
	m = key(t, m, tea.KeyPressMsg{Code: tea.KeyEscape})
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

	plain := ansi.Strip(m.View().Content)
	if !strings.Contains(plain, "Fix auth middleware") || !strings.Contains(plain, "2h ago") {
		t.Errorf("picker row should show the title and its relative age:\n%s", plain)
	}

	for _, line := range strings.Split(plain, "\n") {
		if strings.Contains(line, "Fix auth middleware") && strings.Contains(line, "·") {
			t.Errorf("picker rows carry visual markers now — no · separators:\n%s", line)
		}
	}

	if strings.Contains(plain, "golem · 3 turns") || strings.Contains(plain, "3 turns") {
		t.Errorf("dir and turns are noise on the row:\n%s", plain)
	}

	if strings.Contains(plain, "01JTESTSESSIONID") {
		t.Errorf("raw session id must not render when a title exists:\n%s", plain)
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

	view := m.View().Content
	if n := strings.Count(ansi.Strip(view), "▎"); n != 1 {
		t.Errorf("current-session bar must mark exactly one row, got %d:\n%s", n, view)
	}
	if line := pickerLine(view, "This one"); !strings.Contains(line, "▎") {
		t.Errorf("picker must bar-mark the client's session row, got %q", line)
	}
	if line := pickerLine(view, "Another"); strings.Contains(line, "▎") {
		t.Errorf("only the client's session may carry the bar, got %q", line)
	}
	if m.pickIndex != 0 {
		t.Errorf("pickIndex = %d, want 0 (cursor starts on the current session)", m.pickIndex)
	}
	if strings.Contains(ansi.Strip(view), "▶") {
		t.Errorf("picker rows must not carry a cursor arrow (reverse video is the cursor):\n%s", view)
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

	// Server-reported busy sessions carry the spinner glyph.
	plain := ansi.Strip(m.View().Content)
	if !strings.Contains(plain, "Elsewhere") || !spinnerIn(plain, "Elsewhere") {
		t.Errorf("busy session row must carry a spinner glyph:\n%s", plain)
	}
	if spinnerIn(plain, "Mine") {
		t.Errorf("idle current session must not carry a spinner:\n%s", plain)
	}

	// A local turn active in this client's session spins even when the
	// list snapshot predates the turn.
	m = deliver(t, m, golemclient.TurnStart{
		Type: golemclient.TypeTurnStart, Session: "s1", Turn: "t1",
		Provider: "zai", Model: "glm-5.3",
	})
	plain = ansi.Strip(m.View().Content)
	if !spinnerIn(plain, "Mine") {
		t.Errorf("locally active session row must carry a spinner:\n%s", plain)
	}

	// Turn end clears it.
	m = deliver(t, m, golemclient.TurnEnd{Type: golemclient.TypeTurnEnd, Session: "s1", Turn: "t1"})
	if spinnerIn(ansi.Strip(m.View().Content), "Mine") {
		t.Errorf("turn end must clear the spinner:\n%s", m.View().Content)
	}
}

// pickerLine returns the picker row containing title ("" when absent),
// ANSI-stripped — assertions target the row, not the whole view.
func pickerLine(view, title string) string {
	for _, line := range strings.Split(ansi.Strip(view), "\n") {
		if strings.Contains(line, title) {
			return line
		}
	}

	return ""
}

// spinnerIn reports whether the row containing title carries a spinner
// frame glyph in its status column.
func spinnerIn(plainView, title string) bool {
	for _, line := range strings.Split(plainView, "\n") {
		if strings.Contains(line, title) {
			for _, frame := range kit.SpinnerFrames {
				if strings.Contains(line, frame) {
					return true
				}
			}
		}
	}

	return false
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

	if spinnerIn(ansi.Strip(m.View().Content), "Mine") {
		t.Errorf("stale busy flag must not spin the idle local session:\n%s", m.View().Content)
	}
}

// TestModel_SessionPickerCursorDoesNotShiftRows verifies moving the
// cursor is a pure highlight change: every picker row keeps its exact
// rendered width (no arrow prefix swelling the selected row, no panel
// re-centering shake).
func TestModel_SessionPickerCursorDoesNotShiftRows(t *testing.T) {
	t.Parallel()

	m, _ := wired(t)
	m = resize(m, 80, 30)
	m.listRequest = true
	m = deliver(t, m, golemclient.SessionList{
		Type: golemclient.TypeSessionList,
		Sessions: []golemclient.SessionSummary{
			{ID: "s1", Title: "Alpha task", Cwd: "/a"},
			{ID: "s2", Title: "Beta task with a much much longer title", Cwd: "/b"},
			{ID: "s3", Title: "Gamma", Cwd: "/c"},
		},
	})

	// rowCells maps each title to (line width, text offset) — both must
	// survive a cursor move untouched.
	rowCells := func(m Model) map[string][2]int {
		cells := map[string][2]int{}
		for _, line := range strings.Split(ansi.Strip(m.View().Content), "\n") {
			for _, title := range []string{"Alpha", "Beta", "Gamma"} {
				if at := strings.Index(line, title); at >= 0 {
					cells[title] = [2]int{len([]rune(line)), at}
				}
			}
		}

		return cells
	}

	before := rowCells(m)
	m = key(t, m, tea.KeyPressMsg{Code: tea.KeyDown})
	m = key(t, m, tea.KeyPressMsg{Code: tea.KeyDown})
	after := rowCells(m)

	if len(before) != 3 || len(after) != 3 {
		t.Fatalf("rows not found: before=%v after=%v", before, after)
	}

	for title, cell := range before {
		got := after[title]
		if got[0] != cell[0] {
			t.Errorf("row %q width changed with the cursor moved: %d -> %d (panel shake)",
				title, cell[0], got[0])
		}

		if got[1] != cell[1] {
			t.Errorf("row %q text shifted with the cursor moved: offset %d -> %d",
				title, cell[1], got[1])
		}
	}

	if strings.Contains(ansi.Strip(m.View().Content), "▶") {
		t.Errorf("picker rows must not carry a cursor arrow:\n%s", m.View().Content)
	}
}

// TestModel_SessionPickerSpinnerTicks verifies the picker's spinner
// animates: a tick advances the frame, and ticks stop mattering once the
// picker closes (a stale-generation tick is ignored).
func TestModel_SessionPickerSpinnerTicks(t *testing.T) {
	t.Parallel()

	m, _ := wired(t)
	m = resize(m, 100, 40)
	m = deliver(t, m, golemclient.SessionMeta{Type: golemclient.TypeSessionMeta, Session: "s1"})
	m.listRequest = true
	m = deliver(t, m, golemclient.SessionList{
		Type: golemclient.TypeSessionList,
		Sessions: []golemclient.SessionSummary{
			{ID: "s2", Title: "Elsewhere", Cwd: "/b", Turns: 2, Busy: true},
		},
	})

	// The row spins from frame 0 while the picker is open.
	if !strings.Contains(ansi.Strip(m.View().Content), kit.SpinnerFrames[0]) {
		t.Errorf("picker spinner must start at frame 0:\n%s", m.View().Content)
	}

	next, _ := m.Update(spinTickMsg{gen: m.spinGen})
	m2 := next.(Model)
	if m2.spinFrame != 1 {
		t.Errorf("spinFrame after tick = %d, want 1", m2.spinFrame)
	}
	if !strings.Contains(ansi.Strip(m2.View().Content), kit.SpinnerFrames[1]) {
		t.Errorf("picker spinner must advance to frame 1:\n%s", m2.View().Content)
	}

	// A stale-generation tick (picker reopened since) must not advance.
	next, _ = m2.Update(spinTickMsg{gen: m2.spinGen + 5})
	if got := next.(Model).spinFrame; got != 1 {
		t.Errorf("stale tick advanced the frame to %d, want it ignored", got)
	}
}

// TestModel_SessionPickerSpinnerStopsWhenClosed verifies the spinner
// frame stops advancing once the picker closed (no animation while the
// chat view owns the screen).
func TestModel_SessionPickerSpinnerStopsWhenClosed(t *testing.T) {
	t.Parallel()

	m, _ := wired(t)
	m = resize(m, 100, 40)
	m = deliver(t, m, golemclient.SessionMeta{Type: golemclient.TypeSessionMeta, Session: "s1"})
	m.listRequest = true
	m = deliver(t, m, golemclient.SessionList{
		Type: golemclient.TypeSessionList,
		Sessions: []golemclient.SessionSummary{
			{ID: "s2", Title: "Elsewhere", Cwd: "/b", Turns: 2, Busy: true},
		},
	})
	m = key(t, m, tea.KeyPressMsg{Code: tea.KeyEnter}) // opens the session, closes the picker

	next, _ := m.Update(spinTickMsg{gen: m.spinGen})
	if got := next.(Model).spinFrame; got != 0 {
		t.Errorf("tick advanced a closed picker's frame to %d, want it ignored", got)
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

	view := m.View().Content
	if !strings.Contains(view, "Fix auth middleware") {
		t.Errorf("filter must keep the matching row:\n%s", view)
	}

	for _, gone := range []string{"Add session tests", "Refactor db layer"} {
		if strings.Contains(view, gone) {
			t.Errorf("filter must drop %q:\n%s", gone, view)
		}
	}

	m = key(t, m, tea.KeyPressMsg{Code: tea.KeyEnter})

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

	m = key(t, m, tea.KeyPressMsg{Code: 'r', Mod: tea.ModCtrl})
	if !m.sessRenaming {
		t.Fatalf("ctrl+r must start renaming")
	}

	if got := m.sessRenameText; got != "Fix auth middleware" {
		t.Errorf("rename input = %q, want the current title seeded", got)
	}

	m = key(t, m, tea.KeyPressMsg{Code: 'u', Mod: tea.ModCtrl}) // clear the seeded text
	for _, r := range "New name" {
		m = key(t, m, rkey(r))
	}

	m = key(t, m, tea.KeyPressMsg{Code: tea.KeyEnter})

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

	m = key(t, m, tea.KeyPressMsg{Code: 'r', Mod: tea.ModCtrl})
	m = key(t, m, tea.KeyPressMsg{Code: tea.KeyEscape})

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

	m = key(t, m, tea.KeyPressMsg{Code: tea.KeyEscape})

	if m.mode != modeSessions || m.sessQuery != "" {
		t.Fatalf("first esc must clear the query, mode=%v query=%q", m.mode, m.sessQuery)
	}

	m = key(t, m, tea.KeyPressMsg{Code: tea.KeyEscape})

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

	if !strings.Contains(m.View().Content, "Server title") {
		t.Errorf("session_title event must refresh the open list:\n%s", m.View().Content)
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
	view := m.View().Content
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

	view := m.View().Content
	if !strings.Contains(view, "tail-marker") {
		t.Errorf("replayed transcript should open showing its tail:\n%s", view)
	}
	// Content cannot distinguish top from bottom here (the x block fills
	// both ends and the sticky header pins the opening question by
	// design), so assert the scroll position itself.
	if !m.vp.AtBottom() {
		t.Errorf("history replay should open at the bottom: YOffset=%d height=%d",
			m.vp.YOffset(), m.vp.Height())
	}
}

func TestModel_FollowKeepsLastLineVisibleUnderSticky(t *testing.T) {
	t.Parallel()
	m, _ := wired(t)
	m = resize(m, 80, 24)
	m.blocks = []block{
		{kind: blockUser, text: "question one"},
		{kind: blockAssistant, text: strings.Repeat("answer line\n", 100)},
		{kind: blockUser, text: "question two"},
	}
	m.refreshView()
	m = deliver(t, m, golemclient.TurnStart{Type: golemclient.TypeTurnStart})
	m = deliver(t, m, golemclient.TextDelta{
		Type:  golemclient.TypeTextDelta,
		Delta: strings.Repeat("filler\n", 40) + "tail-marker\n",
	})

	if got := m.stickyText(); got != "question two" {
		t.Fatalf("precondition: sticky must pin question two, got %q", got)
	}
	if !strings.Contains(m.View().Content, "tail-marker") {
		t.Errorf("streaming tail must stay visible under the sticky bar:\n%s", m.View().Content)
	}
	if !m.vp.AtBottom() {
		t.Errorf("follow should keep the viewport at the bottom: YOffset=%d H=%d",
			m.vp.YOffset(), m.vp.Height())
	}
}

func TestModel_ResizeShrinkKeepsTailVisible(t *testing.T) {
	t.Parallel()
	m, _ := wired(t)
	m = resize(m, 80, 30)
	m = deliver(t, m, golemclient.TurnStart{Type: golemclient.TypeTurnStart})
	m = deliver(t, m, golemclient.TextDelta{
		Type:  golemclient.TypeTextDelta,
		Delta: strings.Repeat("filler\n", 60) + "last-line\n",
	})
	m = deliver(t, m, golemclient.TurnEnd{Type: golemclient.TypeTurnEnd})
	if !strings.Contains(m.View().Content, "last-line") {
		t.Fatal("precondition: tail visible before resize")
	}

	m = resize(m, 80, 12)
	if !strings.Contains(m.View().Content, "last-line") {
		t.Errorf("shrinking the terminal must keep the tail on screen:\n%s", m.View().Content)
	}
	if !m.vp.AtBottom() {
		t.Errorf("follow should re-pin after a shrink: YOffset=%d H=%d",
			m.vp.YOffset(), m.vp.Height())
	}
}

func TestModel_ResizeShrinkPreservesScrollback(t *testing.T) {
	t.Parallel()
	m, _ := wired(t)
	m = resize(m, 80, 30)
	m = deliver(t, m, golemclient.TurnStart{Type: golemclient.TypeTurnStart})
	m = deliver(t, m, golemclient.TextDelta{
		Type:  golemclient.TypeTextDelta,
		Delta: strings.Repeat("filler\n", 200),
	})
	m = deliver(t, m, golemclient.TurnEnd{Type: golemclient.TypeTurnEnd})
	for range 4 {
		m = key(t, m, tea.KeyPressMsg{Code: tea.KeyPgUp})
	}
	if m.follow {
		t.Fatal("precondition: paging up suspends follow")
	}

	m = resize(m, 80, 12)
	if strings.Contains(m.View().Content, "filler\nfiller") && m.vp.AtBottom() {
		t.Error("resize must not jump a scrolled-back reader to the tail")
	}
	if m.follow {
		t.Error("resize must not resume auto-follow")
	}
}

func TestModel_ComposerGrowthKeepsTailVisible(t *testing.T) {
	t.Parallel()
	m, _ := wired(t)
	m = resize(m, 80, 14)
	m = deliver(t, m, golemclient.TurnStart{Type: golemclient.TypeTurnStart})
	m = deliver(t, m, golemclient.TextDelta{
		Type:  golemclient.TypeTextDelta,
		Delta: strings.Repeat("filler\n", 30) + "last-line\n",
	})
	m = deliver(t, m, golemclient.TurnEnd{Type: golemclient.TypeTurnEnd})
	if !strings.Contains(m.View().Content, "last-line") {
		t.Fatal("precondition: tail visible before composer growth")
	}

	m.insertNewline()
	m.insertNewline()
	m.insertNewline()
	if m.inputRows < 2 {
		t.Fatalf("precondition: composer must grow, inputRows=%d", m.inputRows)
	}

	if !strings.Contains(m.View().Content, "last-line") {
		t.Errorf("composer growth must keep the tail on screen:\n%s", m.View().Content)
	}
	if !m.vp.AtBottom() {
		t.Errorf("follow should re-pin after composer growth: YOffset=%d H=%d",
			m.vp.YOffset(), m.vp.Height())
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
	view := m.View().Content
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
	view := plain(m.View().Content)
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
	view = m.View().Content
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
	if strings.Contains(m.View().Content, "▎") {
		t.Errorf("sticky should disappear when the transcript clears:\n%s", m.View().Content)
	}
}

func TestModel_HeaderAndSessionStatusGone(t *testing.T) {
	t.Parallel()
	m, _ := wired(t)
	m = resize(m, 80, 24)
	m = deliver(t, m, golemclient.SessionMeta{
		Type: golemclient.TypeSessionMeta, Session: "sess-abcd1234", Turns: 3,
	})
	view := m.View().Content
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
		m = key(t, m, tea.KeyPressMsg{Code: tea.KeyPgUp})
	}
	m = deliver(t, m, golemclient.TextDelta{Type: golemclient.TypeTextDelta, Delta: "mid\n"})
	view := m.View().Content
	if !strings.Contains(view, "head-marker") {
		t.Errorf("manual scrollback should survive streaming deltas:\n%s", view)
	}
	if strings.Contains(view, "mid") {
		t.Errorf("tail should stay off-screen while scrolled back:\n%s", view)
	}

	// Paging back to the bottom resumes the follow.
	for range 20 {
		m = key(t, m, tea.KeyPressMsg{Code: tea.KeyPgDown})
	}

	m = deliver(t, m, golemclient.TextDelta{Type: golemclient.TypeTextDelta, Delta: "tail-marker"})
	if !strings.Contains(m.View().Content, "tail-marker") {
		t.Errorf("reaching the bottom should resume auto-follow:\n%s", m.View().Content)
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
		next, _ := m.Update(tea.MouseWheelMsg{Button: tea.MouseWheelUp})
		m = next.(Model)
	}

	m = deliver(t, m, golemclient.TextDelta{Type: golemclient.TypeTextDelta, Delta: "mid\n"})
	view := m.View().Content
	if !strings.Contains(view, "head-marker") {
		t.Errorf("wheel scrollback should survive streaming deltas:\n%s", view)
	}
	if strings.Contains(view, "mid") {
		t.Errorf("tail should stay off-screen while wheeled back:\n%s", view)
	}

	// Wheel to the bottom resumes the follow.
	for range 40 {
		next, _ := m.Update(tea.MouseWheelMsg{Button: tea.MouseWheelDown})
		m = next.(Model)
	}

	m = deliver(t, m, golemclient.TextDelta{Type: golemclient.TypeTextDelta, Delta: "tail-marker"})
	if !strings.Contains(m.View().Content, "tail-marker") {
		t.Errorf("reaching the bottom should resume auto-follow:\n%s", m.View().Content)
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
	m.vp.SetYOffset(100)
	if got := m.stickyText(); got != "first question" {
		t.Errorf("sticky in first-answer region = %q, want first question", got)
	}

	// Reading the second answer: the sticky switches questions.
	m.vp.SetYOffset(300)
	if got := m.stickyText(); got != "second question" {
		t.Errorf("sticky in second-answer region = %q, want second question", got)
	}

	// A question fully visible in the viewport needs no sticky.
	m.vp.SetYOffset(0)
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

	// Init batches the connect with the background-color request; run
	// the batch and keep the first connErrMsg it yields.
	var connErr connErrMsg

	for _, cmd := range flattenCmds(m.Init()) {
		if msg, ok := cmd().(connErrMsg); ok {
			connErr = msg

			break
		}
	}

	if connErr.err == nil || !strings.Contains(connErr.err.Error(), "no server") {
		t.Fatalf("Init cmds = %#v, want connect error", m.Init())
	}
	next, _ := m.Update(connErr)
	view := next.(Model).View().Content
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

	topRowOf := stickyTopRow // screen rows are padding + sticky aware

	// Screen offsets: top, middle, and deep enough to pin the sticky bar.
	for _, yOff := range []int{0, 3, 8} {
		for _, span := range build().toolSpans {
			m := build()
			m.vp.SetYOffset(yOff)
			m.refreshView()

			topRow := topRowOf(m)
			screenY := topRow + span.start - m.vp.YOffset()

			if screenY-topRow < 0 || screenY-topRow >= m.vp.Height() {
				continue // row scrolled out of the viewport
			}

			// The pre-click view: every tool row collapsed.
			viewLines := strings.Split(m.View().Content, "\n")
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

			next, _ := m.Update(tea.MouseClickMsg{Button: tea.MouseLeft, X: 0, Y: screenY})
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

	idle := strings.Count(m.View().Content, "\n") + 1

	m = deliver(t, m, golemclient.TurnStart{Type: golemclient.TypeTurnStart})
	m = deliver(t, m, golemclient.ToolCallStart{
		Type: golemclient.TypeToolCallStart, CallID: "c1", Tool: "bash",
		Args: []byte(`{"command":"go test ./..."}`),
	})

	if active := strings.Count(m.View().Content, "\n") + 1; active != idle {
		t.Errorf("layout height changed: idle=%d active=%d", idle, active)
	}

	m = deliver(t, m, golemclient.TurnEnd{Type: golemclient.TypeTurnEnd})
	m = deliver(t, m, golemclient.UserMessage{Type: golemclient.TypeUserMessage, Text: "long question over many lines"})

	if scrolled := strings.Count(m.View().Content, "\n") + 1; scrolled != idle {
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
				m = key(t, m, tea.KeyPressMsg{Code: 'p', Mod: tea.ModCtrl})
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
				m = key(t, m, tea.KeyPressMsg{Code: 'r', Mod: tea.ModCtrl})
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

			for i, line := range strings.Split(m.View().Content, "\n") {
				if w := ansi.StringWidth(line); w > tt.width {
					t.Fatalf("row %d width %d exceeds %d:\n%s", i, w, tt.width, m.View().Content)
				}
			}
		})
	}
}

func TestModel_StickyPinsLastLineWithoutTrailingNewline(t *testing.T) {
	t.Parallel()
	m, _ := wired(t)
	m = resize(m, 80, 24)
	m.blocks = []block{
		{kind: blockUser, text: "question one"},
		{kind: blockAssistant, text: strings.Repeat("answer line\n", 60)},
		{kind: blockUser, text: "question two"},
	}
	m.refreshView()
	m = deliver(t, m, golemclient.TurnStart{Type: golemclient.TypeTurnStart})
	// The final delta carries NO trailing newline: its text is the very
	// last content line, with no empty line to sacrifice to the shrink.
	m = deliver(t, m, golemclient.TextDelta{
		Type:  golemclient.TypeTextDelta,
		Delta: strings.Repeat("filler\n", 40) + "tail-marker",
	})

	if m.stickyText() != "question two" {
		t.Fatalf("precondition: sticky must pin question two, got %q", m.stickyText())
	}
	if !strings.Contains(m.View().Content, "tail-marker") {
		t.Errorf("the true last line must stay visible when sticky shrinks the viewport:\n%s", m.View().Content)
	}
}

func TestModel_StickyTailSurvivesResizeShrink(t *testing.T) {
	t.Parallel()
	m, _ := wired(t)
	m = resize(m, 80, 30)
	m = deliver(t, m, golemclient.TurnStart{Type: golemclient.TypeTurnStart})
	m = deliver(t, m, golemclient.TextDelta{
		Type:  golemclient.TypeTextDelta,
		Delta: strings.Repeat("filler\n", 80) + "tail-marker",
	})
	m = deliver(t, m, golemclient.TurnEnd{Type: golemclient.TypeTurnEnd})
	if !strings.Contains(m.View().Content, "tail-marker") {
		t.Fatal("precondition: tail visible before shrink")
	}

	m = resize(m, 80, 14)
	if !strings.Contains(m.View().Content, "tail-marker") {
		t.Errorf("resize shrink must keep the true last line visible under sticky:\n%s", m.View().Content)
	}
}

func TestModel_ManualScrollToBottomShowsLastLine(t *testing.T) {
	t.Parallel()
	m, _ := wired(t)
	m = resize(m, 80, 24)
	m = deliver(t, m, golemclient.TurnStart{Type: golemclient.TypeTurnStart})
	m = deliver(t, m, golemclient.TextDelta{
		Type:  golemclient.TypeTextDelta,
		Delta: strings.Repeat("filler\n", 60) + "tail-marker",
	})
	m = deliver(t, m, golemclient.TurnEnd{Type: golemclient.TypeTurnEnd})
	for range 8 {
		m = key(t, m, tea.KeyPressMsg{Code: tea.KeyPgUp})
	}

	for range 8 {
		m = key(t, m, tea.KeyPressMsg{Code: tea.KeyPgDown})
	}

	if !m.follow {
		t.Error("reaching the bottom should resume follow")
	}
	if !strings.Contains(m.View().Content, "tail-marker") {
		t.Errorf("paging back to the bottom must show the true last line:\n%s", m.View().Content)
	}
}

func TestModel_ToolBlock_ClickLandsOnRenderedRowUnderStickyAtBottom(t *testing.T) {
	t.Parallel()

	m, _ := wired(t)
	m = resize(m, 80, 24)

	// Long answer, then a tool call as the LAST content line: the sticky
	// bar pins (the governing question scrolled off) and the window sits
	// at the tail — the state where the rendered window runs one row
	// ahead of vp.YOffset.
	m.blocks = []block{
		{kind: blockUser, text: "fix the bug"},
		{kind: blockAssistant, text: strings.Repeat("answer line\n", 60)},
		{kind: blockTool, callID: "c1", tool: "read", args: `{"path":"main.go"}`, done: true, ok: true, result: "1: package main"},
	}
	m.refreshView()
	m.vp.GotoBottom()
	m.refreshView()

	if m.stickyText() != "fix the bug" {
		t.Fatalf("precondition: sticky header must be visible, got %q", m.stickyText())
	}
	if !m.vp.AtBottom() {
		t.Fatal("precondition: viewport at the bottom")
	}

	span := m.toolSpans[0]
	top := stickyTopRow(m)
	offset, height := m.renderedWindow()
	if span.start < offset || span.start >= offset+height {
		t.Fatalf("precondition: tool row inside the rendered window: span=%d window=[%d,%d)", span.start, offset, offset+height)
	}

	// Click the tool row's screen position: sticky line + rows into the window.
	next, _ := m.Update(tea.MouseClickMsg{Button: tea.MouseLeft, X: 2, Y: top + span.start - offset})
	m = next.(Model)

	if !m.blocks[2].expanded {
		t.Error("click must land on the rendered tool row, not a row ahead of it")
	}
}

func TestModel_SessionPickerDeleteFlow(t *testing.T) {
	t.Parallel()

	m, fc := wired(t)
	m = resize(m, 100, 40)
	m.listRequest = true
	m = deliver(t, m, golemclient.SessionList{
		Type: golemclient.TypeSessionList,
		Sessions: []golemclient.SessionSummary{
			{ID: "s1", Title: "Fix auth middleware", Cwd: "/a"},
			{ID: "s2", Title: "Add session tests", Cwd: "/b"},
		},
	})

	// First ctrl+d arms; nothing sent; the row shows the confirm hint.
	m = key(t, m, tea.KeyPressMsg{Code: 'd', Mod: tea.ModCtrl})
	if len(fc.sent) != 0 {
		t.Fatalf("armed delete must not send yet: %+v", fc.sent)
	}
	if m.sessDeleteArmed != "s1" {
		t.Errorf("sessDeleteArmed = %q, want s1", m.sessDeleteArmed)
	}
	if !strings.Contains(m.View().Content, "ctrl+d again") {
		t.Errorf("armed row must show the confirm hint:\n%s", m.View().Content)
	}

	// Second ctrl+d sends the delete and removes the row optimistically.
	m = key(t, m, tea.KeyPressMsg{Code: 'd', Mod: tea.ModCtrl})
	sent, ok := lastSent(t, fc).(golemclient.DeleteSession)
	if !ok || sent.Session != "s1" {
		t.Fatalf("sent = %#v, want delete s1", sent)
	}
	if len(m.sessions) != 1 || m.sessions[0].ID != "s2" {
		t.Errorf("sessions after optimistic delete = %+v, want only s2", m.sessions)
	}
	if m.sessDeleteArmed != "" {
		t.Errorf("sessDeleteArmed = %q, want disarmed after firing", m.sessDeleteArmed)
	}
	if strings.Contains(m.View().Content, "Fix auth middleware") {
		t.Errorf("deleted row must leave the picker:\n%s", m.View().Content)
	}
}

func TestModel_SessionPickerDeleteDisarmsOnMoveAndEscape(t *testing.T) {
	t.Parallel()

	m, fc := wired(t)
	m = resize(m, 100, 40)
	m.listRequest = true
	m = deliver(t, m, golemclient.SessionList{
		Type: golemclient.TypeSessionList,
		Sessions: []golemclient.SessionSummary{
			{ID: "s1", Title: "Fix auth middleware", Cwd: "/a"},
			{ID: "s2", Title: "Add session tests", Cwd: "/b"},
		},
	})

	m = key(t, m, tea.KeyPressMsg{Code: 'd', Mod: tea.ModCtrl})
	m = key(t, m, tea.KeyPressMsg{Code: tea.KeyDown})
	if m.sessDeleteArmed != "" {
		t.Errorf("moving the cursor must disarm: %q", m.sessDeleteArmed)
	}

	m = key(t, m, tea.KeyPressMsg{Code: 'd', Mod: tea.ModCtrl})
	m = key(t, m, tea.KeyPressMsg{Code: tea.KeyEscape})
	if m.sessDeleteArmed != "" {
		t.Errorf("esc must disarm: %q", m.sessDeleteArmed)
	}

	if len(fc.sent) != 0 {
		t.Errorf("disarmed deletes never send: %+v", fc.sent)
	}
}

func TestModel_SessionPickerDeleteWrongRowDoesNotFire(t *testing.T) {
	t.Parallel()

	m, fc := wired(t)
	m = resize(m, 100, 40)
	m.listRequest = true
	m = deliver(t, m, golemclient.SessionList{
		Type: golemclient.TypeSessionList,
		Sessions: []golemclient.SessionSummary{
			{ID: "s1", Title: "Fix auth middleware", Cwd: "/a"},
			{ID: "s2", Title: "Add session tests", Cwd: "/b"},
		},
	})

	m = key(t, m, tea.KeyPressMsg{Code: 'd', Mod: tea.ModCtrl}) // arm s1
	m = key(t, m, tea.KeyPressMsg{Code: tea.KeyDown})           // disarms
	m = key(t, m, tea.KeyPressMsg{Code: 'd', Mod: tea.ModCtrl}) // arm s2 now
	m = key(t, m, tea.KeyPressMsg{Code: 'd', Mod: tea.ModCtrl}) // fire on s2

	sent, ok := lastSent(t, fc).(golemclient.DeleteSession)
	if !ok || sent.Session != "s2" {
		t.Fatalf("sent = %#v, want delete s2 (the highlighted row)", sent)
	}
	if len(m.sessions) != 1 || m.sessions[0].ID != "s1" {
		t.Errorf("sessions = %+v, want only s1 left", m.sessions)
	}
}

func TestModel_SessionDeletedEventReconcilesList(t *testing.T) {
	t.Parallel()

	m, fc := wired(t)
	m = resize(m, 100, 40)
	m.sessionID = "s9"
	m = deliver(t, m, golemclient.SessionList{
		Type: golemclient.TypeSessionList,
		Sessions: []golemclient.SessionSummary{
			{ID: "s1", Title: "Fix auth middleware", Cwd: "/a"},
			{ID: "s2", Title: "Add session tests", Cwd: "/b"},
		},
	})

	// Server confirmation for a session this client did not delete
	// optimistically (another client's delete).
	m = deliver(t, m, golemclient.SessionDeleted{
		Type: golemclient.TypeSessionDeleted, Session: "s2",
	})

	if len(m.sessions) != 1 || m.sessions[0].ID != "s1" {
		t.Errorf("sessions = %+v, want s2 dropped on confirmation", m.sessions)
	}

	if len(fc.sent) != 0 {
		t.Errorf("reconciliation must not send: %+v", fc.sent)
	}
}

func TestModel_SessionDeletedCurrentSessionResetsView(t *testing.T) {
	t.Parallel()

	m, _ := wired(t)
	m = resize(m, 100, 40)
	m.sessionID = "s1"
	m.blocks = []block{{kind: blockUser, text: "hello"}}
	m.turnActive = true
	m.refreshView()

	m = deliver(t, m, golemclient.SessionDeleted{
		Type: golemclient.TypeSessionDeleted, Session: "s1",
	})

	if m.sessionID != "" {
		t.Errorf("sessionID = %q, want cleared back to the lazy-start state", m.sessionID)
	}
	if len(m.blocks) != 0 {
		t.Errorf("blocks = %+v, want the transcript cleared", m.blocks)
	}
	if m.turnActive {
		t.Error("deleting the current session must clear the turn flag")
	}
}

func TestModel_SessionPickerFooterMentionsDelete(t *testing.T) {
	t.Parallel()

	m, _ := wired(t)
	m = resize(m, 100, 40)
	m.listRequest = true
	m = deliver(t, m, golemclient.SessionList{
		Type:     golemclient.TypeSessionList,
		Sessions: []golemclient.SessionSummary{{ID: "s1", Title: "Fix auth middleware", Cwd: "/a"}},
	})

	if !strings.Contains(m.View().Content, "ctrl+d delete") {
		t.Errorf("picker footer must mention ctrl+d delete:\n%s", m.View().Content)
	}
}

func TestModel_SessionPickerShowsLiveQuery(t *testing.T) {
	t.Parallel()

	m, _ := wired(t)
	m = resize(m, 100, 40)
	m.listRequest = true
	m = deliver(t, m, golemclient.SessionList{
		Type: golemclient.TypeSessionList,
		Sessions: []golemclient.SessionSummary{
			{ID: "s1", Title: "Fix auth middleware", Cwd: "/a"},
			{ID: "s2", Title: "Add session tests", Cwd: "/b"},
		},
	})

	for _, r := range "fix" {
		m = key(t, m, rkey(r))
	}

	if got := m.View().Content; !strings.Contains(got, "› fix_") {
		t.Errorf("picker must show the live query — nothing renders what the user typed:\n%s", got)
	}

	m = key(t, m, tea.KeyPressMsg{Code: tea.KeyBackspace})
	m = key(t, m, tea.KeyPressMsg{Code: tea.KeyBackspace})
	m = key(t, m, tea.KeyPressMsg{Code: tea.KeyBackspace})

	if got := m.View().Content; !strings.Contains(got, "› _") {
		t.Errorf("backspaces must empty the visible query:\n%s", got)
	}
}

// dayNoon returns local noon on the calendar day offset from now —
// fixed offsets like -20h flip between "yesterday" and "2 days ago"
// depending on when the test runs; noon anchors are DST-safe.
func dayNoon(now time.Time, daysAgo int) time.Time {
	noon := time.Date(now.Year(), now.Month(), now.Day(), 12, 0, 0, 0, time.Local)
	return noon.AddDate(0, 0, -daysAgo)
}

// sessList builds a SessionList with count sessions titled s01..sNN,
// newest first, all last active today.
func sessList(count int, now time.Time) golemclient.SessionList {
	sessions := make([]golemclient.SessionSummary, 0, count)
	for i := range count {
		sessions = append(sessions, golemclient.SessionSummary{
			ID: fmt.Sprintf("s%02d", i+1), Title: fmt.Sprintf("sess%02d", i+1),
			Cwd: "/w", LastActive: now.Add(-time.Duration(i+1) * time.Minute).Format(time.RFC3339),
		})
	}
	return golemclient.SessionList{Type: golemclient.TypeSessionList, Sessions: sessions}
}

func TestModel_SessionPickerScrollsLongLists(t *testing.T) {
	t.Parallel()

	now := time.Now()
	m, _ := wired(t)
	m = resize(m, 100, 40)
	m.listRequest = true
	m = deliver(t, m, sessList(30, now))

	// 30 sessions + headers must not all render: the window is bounded
	// and follows the cursor. Walk to the bottom.
	for range 29 {
		m = key(t, m, tea.KeyPressMsg{Code: tea.KeyDown})
	}

	view := m.View().Content
	if !strings.Contains(view, "sess30") {
		t.Errorf("cursor at the bottom must be visible:\n%s", view)
	}
	if strings.Contains(view, "sess01") {
		t.Errorf("the first row must scroll out of view:\n%s", view)
	}

	// Count rendered session rows: at most the window cap.
	shown := 0
	for i := range 30 {
		if strings.Contains(view, fmt.Sprintf("sess%02d", i+1)) {
			shown++
		}
	}
	if shown > pickerVisibleRows {
		t.Errorf("rendered %d rows, want <= %d", shown, pickerVisibleRows)
	}
}

func TestModel_SessionPickerGroupsByDate(t *testing.T) {
	t.Parallel()

	now := time.Now()
	m, _ := wired(t)
	m = resize(m, 100, 40)
	m.listRequest = true
	m = deliver(t, m, golemclient.SessionList{
		Type: golemclient.TypeSessionList,
		Sessions: []golemclient.SessionSummary{
			{
				ID: "a", Title: "today session", Cwd: "/w",
				LastActive: dayNoon(now, 0).Format(time.RFC3339),
			},
			{
				ID: "b", Title: "yesterday session", Cwd: "/w",
				LastActive: dayNoon(now, 1).Format(time.RFC3339),
			},
			{
				ID: "c", Title: "older session", Cwd: "/w",
				LastActive: dayNoon(now, 3).Format(time.RFC3339),
			},
		},
	})

	view := m.View().Content

	// Headers appear once each, in newest-first order, before their rows.
	for _, label := range []string{"Today", "Yesterday"} {
		if n := strings.Count(view, label); n != 1 {
			t.Errorf("header %q appears %d times, want 1:\n%s", label, n, view)
		}
	}
	if idx := strings.Index(view, "today session"); idx == -1 || strings.LastIndex(view, "Today") > idx {
		t.Errorf("the Today header must precede its rows:\n%s", view)
	}
	if idx := strings.Index(view, "yesterday session"); idx == -1 || !strings.Contains(view[:idx], "Yesterday") {
		t.Errorf("the Yesterday header must precede its rows:\n%s", view)
	}
}

func TestModel_SessionPickerGroupHeaderHidesWhenScrolled(t *testing.T) {
	t.Parallel()

	now := time.Now()
	m, _ := wired(t)
	m = resize(m, 100, 40)
	m.listRequest = true
	m = deliver(t, m, sessList(30, now))

	for range 29 {
		m = key(t, m, tea.KeyPressMsg{Code: tea.KeyDown})
	}

	// The Today header sits above the window at the bottom.
	if strings.Contains(m.View().Content, "Today") {
		t.Errorf("a header scrolled out of the window must not render:\n%s", m.View().Content)
	}

	// Back to the top: the header returns.
	for range 29 {
		m = key(t, m, tea.KeyPressMsg{Code: tea.KeyUp})
	}

	if !strings.Contains(m.View().Content, "Today") {
		t.Errorf("scrolling to the top must bring the header back:\n%s", m.View().Content)
	}
}

func TestModel_SessionPickerWheelScrollsList(t *testing.T) {
	t.Parallel()

	now := time.Now()
	m, _ := wired(t)
	m = resize(m, 100, 40)
	m.listRequest = true
	m = deliver(t, m, sessList(30, now))

	// The wheel scrolls the window without moving the cursor.
	next, _ := m.Update(tea.MouseWheelMsg{Button: tea.MouseWheelDown})
	m = next.(Model)

	if strings.Contains(m.View().Content, "sess01") {
		t.Errorf("wheel down must scroll the first row out:\n%s", m.View().Content)
	}
	if !strings.Contains(m.View().Content, "sess04") {
		t.Errorf("wheel down must reveal lower rows:\n%s", m.View().Content)
	}

	next, _ = m.Update(tea.MouseWheelMsg{Button: tea.MouseWheelUp})
	m = next.(Model)

	if !strings.Contains(m.View().Content, "sess01") {
		t.Errorf("wheel up must scroll back to the first row:\n%s", m.View().Content)
	}
}
