package tui

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"

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

func TestModel_ApprovalPromptYesSendsAllow(t *testing.T) {
	t.Parallel()
	m, fc := wired(t)
	m = deliver(t, m, golemclient.ApprovalRequest{
		Type: golemclient.TypeApprovalRequest, Session: "s1", Turn: "t1",
		ApprovalID: "ap1", CallID: "c1", Tool: "bash", Preview: "rm -rf /tmp/x",
	})
	if m.mode != modeApproval {
		t.Fatalf("mode = %v, want approval", m.mode)
	}
	if !strings.Contains(m.View(), "rm -rf /tmp/x") {
		t.Errorf("footer should show preview:\n%s", m.View())
	}
	m = key(t, m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'y'}})
	resp, ok := lastSent(t, fc).(golemclient.ApprovalResponse)
	if !ok || resp.Decision != "allow" || resp.ApprovalID != "ap1" {
		t.Errorf("sent = %#v, want allow ap1", resp)
	}
	if m.mode != modeChat {
		t.Errorf("mode after answer = %v, want chat", m.mode)
	}
}

func TestModel_ApprovalPromptOtherKeysDeny(t *testing.T) {
	t.Parallel()
	m, fc := wired(t)
	m = deliver(t, m, golemclient.ApprovalRequest{
		Type: golemclient.TypeApprovalRequest, Session: "s1", Turn: "t1", ApprovalID: "ap1",
	})
	key(t, m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'n'}})
	resp, ok := lastSent(t, fc).(golemclient.ApprovalResponse)
	if !ok || resp.Decision != "deny" {
		t.Errorf("sent = %#v, want deny", resp)
	}
}

func TestModel_ToolCallCollapsedByDefault_ExpandableViaTab(t *testing.T) {
	t.Parallel()
	m, _ := wired(t)
	m = resize(m, 100, 40)
	m = deliver(t, m, golemclient.ToolCallStart{
		Type: golemclient.TypeToolCallStart, CallID: "c1", Tool: "read",
		Args: []byte(`{"path":"main.go"}`),
	})
	m = deliver(t, m, golemclient.ToolCallEnd{
		Type: golemclient.TypeToolCallEnd, CallID: "c1", OK: true,
		Result: "1: package main\n2: func main() {}",
	})
	view := m.View()
	if !strings.Contains(view, "read") || !strings.Contains(view, "✓") {
		t.Errorf("collapsed summary missing tool/status:\n%s", view)
	}
	if strings.Contains(view, "package main") {
		t.Errorf("result should be hidden while collapsed:\n%s", view)
	}
	next, _ := m.Update(tea.KeyMsg{Type: tea.KeyTab})
	m = next.(Model)
	if !strings.Contains(m.View(), "package main") {
		t.Errorf("expanded block should show result:\n%s", m.View())
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
	if !strings.Contains(m.View(), "old /a (1 turns)") {
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
		t.Errorf("sticky line should render under the header:\n%s", view)
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
