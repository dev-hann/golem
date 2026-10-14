package server_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/dev-hann/golem/internal/llm"
	"github.com/dev-hann/golem/internal/server"
	"github.com/dev-hann/golem/internal/session"
	"github.com/dev-hann/golem/pkg/golemclient"
)

// scriptedProvider streams queued rounds; streamFn overrides per-round
// behavior (used for hang/gate scenarios). Title side calls get a canned
// stream without consuming a round — titleEvents overrides that stream
// (failure scenarios), and titleFailures fails the first N title calls
// before that override applies (retry scenarios).
type scriptedProvider struct {
	next          int
	rounds        [][]llm.Event
	titleEvents   []llm.Event
	titleFailures int
	titleCalls    int
	streamFn      func(ctx context.Context, round int) <-chan llm.Event
}

func (p *scriptedProvider) Stream(
	ctx context.Context,
	req llm.Request,
) (<-chan llm.Event, error) {
	if req.SystemPrompt == server.TitleSystemPrompt {
		call := p.titleCalls
		p.titleCalls++

		if call < p.titleFailures {
			out := make(chan llm.Event, 1)
			out <- llm.Event{Type: llm.EventError, Err: fmt.Errorf("title call %d failed", call+1)}
			close(out)

			return out, nil
		}

		events := p.titleEvents
		if events == nil {
			events = []llm.Event{{Type: llm.EventTextDelta, Delta: "Test title"}}
		}

		out := make(chan llm.Event, len(events)+1)

		for _, ev := range events {
			out <- ev
		}

		close(out)

		return out, nil
	}

	round := p.next
	p.next++
	if p.streamFn != nil {
		return p.streamFn(ctx, round), nil
	}
	if round >= len(p.rounds) {
		return nil, fmt.Errorf("script: no round %d", round)
	}
	events := p.rounds[round]
	out := make(chan llm.Event, len(events)+1)
	for _, ev := range events {
		out <- ev
	}
	close(out)
	return out, nil
}

// harness wires a server over a unix socket and returns a connected client.
type harness struct {
	t        *testing.T
	srv      *server.Server
	client   *golemclient.Client
	sockDir  string
	storeDir string
	store    *session.Store
	root     string
}

// shortTempDir returns a temp dir whose full path stays under the macOS
// unix-socket limit (sun_path, 104 bytes). t.TempDir() embeds the test name
// and overflows on macOS, making every listen fail with EINVAL.
func shortTempDir(t *testing.T) string {
	t.Helper()

	dir, err := os.MkdirTemp("", "golem")
	if err != nil {
		t.Fatalf("short temp dir: %v", err)
	}

	t.Cleanup(func() { _ = os.RemoveAll(dir) })

	return dir
}

func newHarness(t *testing.T, provider llm.Provider) *harness {
	t.Helper()
	root := t.TempDir()
	storeDir := filepath.Join(t.TempDir(), "sessions")
	store, err := session.NewStore(storeDir, 8)
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	cfg := server.Config{
		Provider: provider, Model: "stub-model", Root: root,
		Store: store, Version: "test-version",
	}
	srv, err := server.New(cfg)
	if err != nil {
		t.Fatalf("server.New: %v", err)
	}
	sockDir := shortTempDir(t)
	listener, err := (&net.ListenConfig{}).Listen(context.Background(), "unix", filepath.Join(sockDir, "golem.sock"))
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	go func() { _ = srv.Serve(listener) }()
	client, err := golemclient.Dial(filepath.Join(sockDir, "golem.sock"))
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	h := &harness{
		t: t, srv: srv, client: client, sockDir: sockDir, storeDir: storeDir,
		store: store, root: root,
	}
	t.Cleanup(func() {
		_ = client.Close()
		srv.Close()
		_ = listener.Close()
	})
	return h
}

// createSession creates a session and returns its id.
func (h *harness) createSession() string {
	h.t.Helper()
	if err := h.client.Send(golemclient.CreateSession{
		Type: golemclient.TypeCreateSession, Cwd: h.root,
	}); err != nil {
		h.t.Fatalf("send create_session: %v", err)
	}
	meta := h.expectType(golemclient.TypeSessionMeta).(golemclient.SessionMeta)
	return meta.Session
}

// expectTypeError reads events until an error event with the wanted code
// arrives, failing on anything else.
func (h *harness) expectTypeError(code string) error {
	h.t.Helper()

	deadline := time.Now().Add(15 * time.Second)
	for time.Now().Before(deadline) {
		ev, got, err := h.client.ReceiveWithTimeout(2 * time.Second)
		if err != nil || !got {
			continue
		}

		if e, ok := ev.(golemclient.ErrorEvent); ok && e.Code == code {
			return nil
		}
	}

	return fmt.Errorf("timed out waiting for error %q", code)
}

// expectType reads events until one of the wanted type arrives.
func (h *harness) expectType(typ string) any {
	h.t.Helper()
	deadline := time.Now().Add(15 * time.Second)
	for time.Now().Before(deadline) {
		ev, got, err := h.client.ReceiveWithTimeout(2 * time.Second)
		if err != nil || !got {
			continue
		}
		if typeName(h.t, ev) == typ {
			return ev
		}
		if e, ok := ev.(golemclient.ErrorEvent); ok && typ != golemclient.TypeError {
			h.t.Fatalf("unexpected error event: %+v", e)
		}
	}
	h.t.Fatalf("timed out waiting for %s", typ)
	return nil
}

// typeName extracts the wire type of a decoded event.
func typeName(t *testing.T, ev any) string {
	t.Helper()
	data, err := json.Marshal(ev)
	if err != nil {
		t.Fatalf("marshal event: %v", err)
	}
	var env golemclient.Envelope
	if err := json.Unmarshal(data, &env); err != nil {
		t.Fatalf("unmarshal envelope: %v", err)
	}
	return env.Type
}

func TestServer_SetModelOverridesNextTurn(t *testing.T) {
	t.Parallel()

	provider := &scriptedProvider{rounds: [][]llm.Event{
		{{Type: llm.EventTextDelta, Delta: "one"}, {Type: llm.EventUsage}},
		{{Type: llm.EventTextDelta, Delta: "two"}, {Type: llm.EventUsage}},
	}}
	h := newHarness(t, provider)
	sess := h.createSession()

	if err := h.client.Send(golemclient.UserMessage{
		Type: golemclient.TypeUserMessage, Session: sess, Text: "hi",
	}); err != nil {
		t.Fatalf("send: %v", err)
	}

	first := h.expectType(golemclient.TypeTurnStart).(golemclient.TurnStart)
	if first.Model != "stub-model" {
		t.Fatalf("first turn model = %q, want the configured one", first.Model)
	}

	h.expectType(golemclient.TypeTurnEnd)

	if err := h.client.Send(golemclient.SetModel{
		Type: golemclient.TypeSetModel, Session: sess, Model: "glm-5.3-flash",
	}); err != nil {
		t.Fatalf("send set_model: %v", err)
	}

	if err := h.client.Send(golemclient.UserMessage{
		Type: golemclient.TypeUserMessage, Session: sess, Text: "again",
	}); err != nil {
		t.Fatalf("send: %v", err)
	}

	second := h.expectType(golemclient.TypeTurnStart).(golemclient.TurnStart)
	if second.Model != "glm-5.3-flash" {
		t.Errorf("second turn model = %q, want the override", second.Model)
	}
}

func TestServer_SetModelRejectsEmptyAndUnknownSession(t *testing.T) {
	t.Parallel()

	h := newHarness(t, &scriptedProvider{})
	sess := h.createSession()

	if err := h.client.Send(golemclient.SetModel{
		Type: golemclient.TypeSetModel, Session: sess, Model: "  ",
	}); err != nil {
		t.Fatalf("send: %v", err)
	}

	if err := h.expectTypeError("invalid_request"); err != nil {
		t.Fatalf("empty model: %v", err)
	}

	if err := h.client.Send(golemclient.SetModel{
		Type: golemclient.TypeSetModel, Session: "missing", Model: "m",
	}); err != nil {
		t.Fatalf("send: %v", err)
	}

	if err := h.expectTypeError("session_not_found"); err != nil {
		t.Fatalf("unknown session: %v", err)
	}
}

func TestServer_UserMessageProducesTurnLifecycle(t *testing.T) {
	t.Parallel()
	provider := &scriptedProvider{rounds: [][]llm.Event{{
		{Type: llm.EventTextDelta, Delta: "answer"},
		{Type: llm.EventUsage, InputTokens: 3, OutputTokens: 2},
	}}}
	h := newHarness(t, provider)
	sess := h.createSession()
	if err := h.client.Send(golemclient.UserMessage{
		Type: golemclient.TypeUserMessage, Session: sess, Text: "hi",
	}); err != nil {
		t.Fatalf("send: %v", err)
	}
	start := h.expectType(golemclient.TypeTurnStart).(golemclient.TurnStart)
	if start.Session != sess || start.Turn == "" || start.Model != "stub-model" {
		t.Errorf("turn_start = %+v", start)
	}
	delta := h.expectType(golemclient.TypeTextDelta).(golemclient.TextDelta)
	if delta.Delta != "answer" || delta.Turn != start.Turn {
		t.Errorf("text_delta = %+v", delta)
	}
	end := h.expectType(golemclient.TypeTurnEnd).(golemclient.TurnEnd)
	if end.Usage.InputTokens != 3 || end.Usage.OutputTokens != 2 {
		t.Errorf("turn_end usage = %+v", end.Usage)
	}
}

func TestServer_BashRunsDirectly_NoApprovalRoundTrip(t *testing.T) {
	t.Parallel()
	provider := &scriptedProvider{rounds: [][]llm.Event{
		{{
			Type: llm.EventToolCalls,
			ToolCalls: []llm.ToolCall{{
				ID: "c1", Name: "bash",
				Args: json.RawMessage(`{"command":"echo golem-auto-marker"}`),
			}},
		}},
		{{Type: llm.EventTextDelta, Delta: "ran"}},
	}}
	h := newHarness(t, provider)
	sess := h.createSession()
	if err := h.client.Send(golemclient.UserMessage{
		Type: golemclient.TypeUserMessage, Session: sess, Text: "run it",
	}); err != nil {
		t.Fatalf("send: %v", err)
	}
	end := h.expectType(golemclient.TypeToolCallEnd).(golemclient.ToolCallEnd)
	if !end.OK || !strings.Contains(end.Result, "golem-auto-marker") {
		t.Errorf("bash should run without approval, got %+v", end)
	}
	h.expectType(golemclient.TypeTurnEnd)
}

func TestServer_InterruptEndsTurnGracefully(t *testing.T) {
	release := make(chan struct{})
	provider := &scriptedProvider{}
	provider.streamFn = func(ctx context.Context, _ int) <-chan llm.Event {
		out := make(chan llm.Event, 1)
		go func() {
			defer close(out)
			<-ctx.Done()
			out <- llm.Event{Type: llm.EventError, Err: ctx.Err()}
		}()
		return out
	}
	h := newHarness(t, provider)
	sess := h.createSession()
	if err := h.client.Send(golemclient.UserMessage{
		Type: golemclient.TypeUserMessage, Session: sess, Text: "stall",
	}); err != nil {
		t.Fatalf("send: %v", err)
	}
	h.expectType(golemclient.TypeTurnStart)
	if err := h.client.Send(golemclient.Interrupt{
		Type: golemclient.TypeInterrupt, Session: sess,
	}); err != nil {
		t.Fatalf("send interrupt: %v", err)
	}
	h.expectType(golemclient.TypeError)
	close(release)
	// Session must accept a new turn afterwards.
	provider.streamFn = nil
	provider.rounds = [][]llm.Event{
		{}, // round 0 was served by streamFn above
		{{Type: llm.EventTextDelta, Delta: "fresh"}},
	}
	if err := h.client.Send(golemclient.UserMessage{
		Type: golemclient.TypeUserMessage, Session: sess, Text: "again",
	}); err != nil {
		t.Fatalf("send second message: %v", err)
	}
	h.expectType(golemclient.TypeTurnStart)
	h.expectType(golemclient.TypeTurnEnd)
}

func TestServer_BusySessionRejectsSecondTurn(t *testing.T) {
	provider := &scriptedProvider{}
	provider.streamFn = func(ctx context.Context, _ int) <-chan llm.Event {
		out := make(chan llm.Event, 1)
		go func() {
			defer close(out)
			<-ctx.Done()
			out <- llm.Event{Type: llm.EventError, Err: ctx.Err()}
		}()
		return out
	}
	h := newHarness(t, provider)
	sess := h.createSession()
	send := func() {
		t.Helper()
		if err := h.client.Send(golemclient.UserMessage{
			Type: golemclient.TypeUserMessage, Session: sess, Text: "x",
		}); err != nil {
			t.Fatalf("send: %v", err)
		}
	}
	send()
	h.expectType(golemclient.TypeTurnStart)
	send()
	errEv := h.expectType(golemclient.TypeError).(golemclient.ErrorEvent)
	if errEv.Code != "invalid_request" {
		t.Errorf("busy code = %s, want invalid_request", errEv.Code)
	}
	if err := h.client.Send(golemclient.Interrupt{
		Type: golemclient.TypeInterrupt, Session: sess,
	}); err != nil {
		t.Fatalf("interrupt: %v", err)
	}
	h.expectType(golemclient.TypeError) // cancellation error from the turn
}

// TestServer_SessionListReportsBusyDuringTurn verifies the session_list
// marks sessions with an in-flight turn busy: a second connection listing
// mid-turn must see busy=true on the running session and busy=false once
// the turn ends.
func TestServer_SessionListReportsBusyDuringTurn(t *testing.T) {
	t.Parallel()

	gate := make(chan struct{})
	provider := &scriptedProvider{}
	provider.streamFn = func(_ context.Context, _ int) <-chan llm.Event {
		out := make(chan llm.Event, 2)
		out <- llm.Event{Type: llm.EventTextDelta, Delta: "working"}
		go func() {
			defer close(out)
			<-gate
			out <- llm.Event{Type: llm.EventUsage, InputTokens: 1, OutputTokens: 1}
		}()
		return out
	}
	h := newHarness(t, provider)
	sess := h.createSession()

	if err := h.client.Send(golemclient.UserMessage{
		Type: golemclient.TypeUserMessage, Session: sess, Text: "hi",
	}); err != nil {
		t.Fatalf("send: %v", err)
	}
	h.expectType(golemclient.TypeTextDelta)

	busy := h.listSessionByID(t, sess)
	if !busy {
		t.Errorf("busy = false mid-turn, want true")
	}

	close(gate)
	h.expectType(golemclient.TypeTurnEnd)

	if busy := h.listSessionByID(t, sess); busy {
		t.Errorf("busy = true after turn end, want false")
	}
}

// A load mid-turn must announce the live turn in session_meta, so a
// second client resuming the session can restore its status line from
// the replay instead of rendering idle while a turn streams.
func TestServer_LoadMidTurnReportsBusyInMeta(t *testing.T) {
	t.Parallel()

	gate := make(chan struct{})
	provider := &scriptedProvider{}
	provider.streamFn = func(_ context.Context, _ int) <-chan llm.Event {
		out := make(chan llm.Event, 2)
		out <- llm.Event{Type: llm.EventTextDelta, Delta: "working"}
		go func() {
			defer close(out)
			<-gate
			out <- llm.Event{Type: llm.EventUsage, InputTokens: 1, OutputTokens: 1}
		}()
		return out
	}
	h := newHarness(t, provider)
	sess := h.createSession()

	second, err := golemclient.Dial(filepath.Join(h.sockDir, "golem.sock"))
	if err != nil {
		t.Fatalf("second dial: %v", err)
	}
	defer second.Close() //nolint:errcheck // test teardown

	if err := h.client.Send(golemclient.UserMessage{
		Type: golemclient.TypeUserMessage, Session: sess, Text: "hi",
	}); err != nil {
		t.Fatalf("send: %v", err)
	}
	h.expectType(golemclient.TypeTextDelta)

	// The second connection loads the busy session: meta must carry
	// busy=true and the replay must end without a turn_end (the turn is
	// still in flight).
	if err := second.Send(golemclient.LoadSession{
		Type: golemclient.TypeLoadSession, Session: sess,
	}); err != nil {
		t.Fatalf("send load: %v", err)
	}

	meta := expectFrom(t, second, golemclient.TypeSessionMeta).(golemclient.SessionMeta)
	if !meta.Busy {
		t.Error("session_meta.busy = false mid-turn, want true")
	}

	hist := expectFrom(t, second, golemclient.TypeHistory).(golemclient.History)
	var sawTurnStart, sawTurnEnd bool
	for _, raw := range hist.Events {
		switch {
		case strings.Contains(string(raw), `"turn_start"`):
			sawTurnStart = true
		case strings.Contains(string(raw), `"turn_end"`):
			sawTurnEnd = true
		}
	}
	if !sawTurnStart || sawTurnEnd {
		t.Errorf("mid-turn replay = turn_start:%v turn_end:%v, want start without end",
			sawTurnStart, sawTurnEnd)
	}

	close(gate)
	h.expectType(golemclient.TypeTurnEnd)
}

// expectFrom reads events from one client until the wanted type arrives.
func expectFrom(t *testing.T, c *golemclient.Client, typ string) any {
	t.Helper()

	deadline := time.Now().Add(15 * time.Second)
	for time.Now().Before(deadline) {
		ev, got, err := c.ReceiveWithTimeout(2 * time.Second)
		if err != nil || !got {
			continue
		}
		if typeName(t, ev) == typ {
			return ev
		}
	}

	t.Fatalf("timed out waiting for %s", typ)
	return nil
}

// listSessionByID lists sessions and returns the busy flag of the wanted
// one; missing sessions fail the test.
func (h *harness) listSessionByID(t *testing.T, id string) bool {
	t.Helper()

	if err := h.client.Send(golemclient.ListSessions{Type: golemclient.TypeListSessions}); err != nil {
		t.Fatalf("send: %v", err)
	}
	list := h.expectType(golemclient.TypeSessionList).(golemclient.SessionList)
	for _, s := range list.Sessions {
		if s.ID == id {
			return s.Busy
		}
	}
	t.Fatalf("session %s missing from list: %+v", id, list.Sessions)
	return false
}

func TestServer_SessionListOrdersByLastActive(t *testing.T) {
	t.Parallel()
	provider := &scriptedProvider{rounds: [][]llm.Event{
		{{Type: llm.EventTextDelta, Delta: "hi"}},
	}}
	h := newHarness(t, provider)
	first := h.createSession()
	time.Sleep(5 * time.Millisecond)
	second := h.createSession()
	if err := h.client.Send(golemclient.ListSessions{Type: golemclient.TypeListSessions}); err != nil {
		t.Fatalf("send: %v", err)
	}
	list := h.expectType(golemclient.TypeSessionList).(golemclient.SessionList)
	if len(list.Sessions) < 2 {
		t.Fatalf("sessions = %+v", list.Sessions)
	}
	if list.Sessions[0].ID != second || list.Sessions[1].ID != first {
		t.Errorf("order = [%s, %s], want newest first", list.Sessions[0].ID, list.Sessions[1].ID)
	}
}

func TestServer_IgnoresUnknownEventTypes(t *testing.T) {
	t.Parallel()
	provider := &scriptedProvider{rounds: [][]llm.Event{}}
	h := newHarness(t, provider)
	if err := h.client.Send(map[string]string{"type": "totally_unknown", "x": "1"}); err != nil {
		t.Fatalf("send unknown: %v", err)
	}
	sess := h.createSession() // connection still healthy
	if sess == "" {
		t.Fatal("empty session id")
	}
}

func TestServer_LoadSessionReplaysHistory(t *testing.T) {
	t.Parallel()
	provider := &scriptedProvider{rounds: [][]llm.Event{
		{{Type: llm.EventTextDelta, Delta: "one"}},
	}}
	h := newHarness(t, provider)
	sess := h.createSession()
	if err := h.client.Send(golemclient.UserMessage{
		Type: golemclient.TypeUserMessage, Session: sess, Text: "hello",
	}); err != nil {
		t.Fatalf("send: %v", err)
	}
	h.expectType(golemclient.TypeTurnEnd)
	if err := h.client.Send(golemclient.LoadSession{
		Type: golemclient.TypeLoadSession, Session: sess,
	}); err != nil {
		t.Fatalf("send load: %v", err)
	}
	meta := h.expectType(golemclient.TypeSessionMeta).(golemclient.SessionMeta)
	if meta.Turns != 1 {
		t.Errorf("meta turns = %d, want 1", meta.Turns)
	}
	hist := h.expectType(golemclient.TypeHistory).(golemclient.History)
	var sawUser, sawDelta bool
	for _, raw := range hist.Events {
		if strings.Contains(string(raw), `"user_message"`) {
			sawUser = true
		}
		if strings.Contains(string(raw), `"text_delta"`) {
			sawDelta = true
		}
	}
	if !sawUser || !sawDelta {
		t.Errorf("history missing events (user=%v delta=%v): %d lines", sawUser, sawDelta, len(hist.Events))
	}
}

func TestServer_EventsPersistedBeforeEmission(t *testing.T) {
	t.Parallel()
	gate := make(chan struct{})
	provider := &scriptedProvider{}
	provider.streamFn = func(_ context.Context, _ int) <-chan llm.Event {
		out := make(chan llm.Event, 2)
		out <- llm.Event{Type: llm.EventTextDelta, Delta: "persisted"}
		go func() {
			defer close(out)
			<-gate
			out <- llm.Event{Type: llm.EventUsage, InputTokens: 1, OutputTokens: 1}
		}()
		return out
	}
	h := newHarness(t, provider)
	sess := h.createSession()
	if err := h.client.Send(golemclient.UserMessage{
		Type: golemclient.TypeUserMessage, Session: sess, Text: "hi",
	}); err != nil {
		t.Fatalf("send: %v", err)
	}
	// The delta arrives on the wire but the turn is gated open; by then the
	// delta line must already be on disk.
	h.expectType(golemclient.TypeTextDelta)
	jsonlPath := filepath.Join(h.storeDir, sess+".jsonl")
	data, err := os.ReadFile(jsonlPath)
	if err != nil {
		t.Fatalf("read session file: %v", err)
	}
	if !strings.Contains(string(data), "persisted") {
		t.Error("text_delta not persisted before turn end; storage must precede emission")
	}
	close(gate)
	h.expectType(golemclient.TypeTurnEnd)
}

func TestServer_FirstTurnGeneratesTitleOnce(t *testing.T) {
	t.Parallel()

	provider := &scriptedProvider{rounds: [][]llm.Event{
		{{Type: llm.EventTextDelta, Delta: "one"}, {Type: llm.EventUsage}},
		{{Type: llm.EventTextDelta, Delta: "two"}, {Type: llm.EventUsage}},
	}}
	h := newHarness(t, provider)
	sess := h.createSession()

	if err := h.client.Send(golemclient.UserMessage{
		Type: golemclient.TypeUserMessage, Session: sess, Text: "fix the auth middleware",
	}); err != nil {
		t.Fatalf("send: %v", err)
	}

	h.expectType(golemclient.TypeTurnStart)
	h.expectType(golemclient.TypeTurnEnd)

	// The title lands asynchronously; poll the store for the record.
	deadline := time.Now().Add(2 * time.Second)

	for time.Now().Before(deadline) {
		summaries, err := h.store.List()
		if err == nil && len(summaries) == 1 && summaries[0].Title == "Test title" {
			break
		}

		time.Sleep(10 * time.Millisecond)
	}

	summaries, err := h.store.List()
	if err != nil {
		t.Fatalf("List: %v", err)
	}

	if len(summaries) != 1 || summaries[0].Title != "Test title" {
		t.Fatalf("summaries = %+v, want the generated title", summaries)
	}

	// A second turn must not regenerate: still exactly one title record.
	if err := h.client.Send(golemclient.UserMessage{
		Type: golemclient.TypeUserMessage, Session: sess, Text: "again",
	}); err != nil {
		t.Fatalf("send: %v", err)
	}

	h.expectType(golemclient.TypeTurnStart)
	h.expectType(golemclient.TypeTurnEnd)

	time.Sleep(100 * time.Millisecond)

	raw, err := os.ReadFile(filepath.Join(h.storeDir, sess+".jsonl"))
	if err != nil {
		t.Fatalf("read session file: %v", err)
	}

	if n := strings.Count(string(raw), `"session_title"`); n != 1 {
		t.Errorf("session_title records = %d, want 1:\n%s", n, raw)
	}
}

func TestServer_RenameSession(t *testing.T) {
	t.Parallel()

	provider := &scriptedProvider{rounds: [][]llm.Event{}}
	h := newHarness(t, provider)
	sess := h.createSession()

	if err := h.client.Send(golemclient.RenameSession{
		Type: golemclient.TypeRenameSession, Session: sess, Title: "  Manual title  ",
	}); err != nil {
		t.Fatalf("send: %v", err)
	}

	title := h.expectType(golemclient.TypeSessionTitle).(golemclient.SessionTitle)
	if title.Title != "Manual title" {
		t.Errorf("title = %q, want the trimmed rename", title.Title)
	}

	summaries, err := h.store.List()
	if err != nil {
		t.Fatalf("List: %v", err)
	}

	if len(summaries) != 1 || summaries[0].Title != "Manual title" {
		t.Errorf("summaries = %+v, want the renamed title persisted", summaries)
	}
}

func TestServer_RenameSessionRejectsEmptyAndUnknownSession(t *testing.T) {
	t.Parallel()

	h := newHarness(t, &scriptedProvider{rounds: [][]llm.Event{}})
	sess := h.createSession()

	if err := h.client.Send(golemclient.RenameSession{
		Type: golemclient.TypeRenameSession, Session: sess, Title: "   ",
	}); err != nil {
		t.Fatalf("send empty: %v", err)
	}

	if err := h.expectTypeError("invalid_request"); err != nil {
		t.Fatalf("empty title: %v", err)
	}

	if err := h.client.Send(golemclient.RenameSession{
		Type: golemclient.TypeRenameSession, Session: "01JUNKNOWNSESSION0000000", Title: "x",
	}); err != nil {
		t.Fatalf("send unknown: %v", err)
	}

	if err := h.expectTypeError("session_not_found"); err != nil {
		t.Fatalf("unknown session: %v", err)
	}
}

func TestServer_VersionQueryAnswersHello(t *testing.T) {
	t.Parallel()

	h := newHarness(t, &scriptedProvider{rounds: [][]llm.Event{}})

	if err := h.client.Send(golemclient.VersionQuery{Type: golemclient.TypeVersionQuery}); err != nil {
		t.Fatalf("send: %v", err)
	}

	hello := h.expectType(golemclient.TypeServerHello).(golemclient.ServerHello)
	if hello.Version != "test-version" {
		t.Errorf("version = %q, want the configured build version", hello.Version)
	}
}

func TestServer_TitleFailureLeavesNoRecord(t *testing.T) {
	t.Parallel()

	provider := &scriptedProvider{
		rounds: [][]llm.Event{
			{{Type: llm.EventTextDelta, Delta: "hi"}, {Type: llm.EventUsage}},
			{{Type: llm.EventTextDelta, Delta: "retry turn"}, {Type: llm.EventUsage}},
		},
		titleFailures: 1,
	}
	h := newHarness(t, provider)
	sess := h.createSession()

	if err := h.client.Send(golemclient.UserMessage{
		Type: golemclient.TypeUserMessage, Session: sess, Text: "hello",
	}); err != nil {
		t.Fatalf("send: %v", err)
	}

	h.expectType(golemclient.TypeTurnStart)
	h.expectType(golemclient.TypeTurnEnd)

	time.Sleep(100 * time.Millisecond)

	raw, err := os.ReadFile(filepath.Join(h.storeDir, sess+".jsonl"))
	if err != nil {
		t.Fatalf("read session file: %v", err)
	}

	if strings.Contains(string(raw), "session_title") {
		t.Errorf("failed title generation must not persist a record:\n%s", raw)
	}

	// The failed side call schedules a retry: the next turn's completion
	// must attempt generation again (the session is still untitled).
	if err := h.client.Send(golemclient.UserMessage{
		Type: golemclient.TypeUserMessage, Session: sess, Text: "second turn",
	}); err != nil {
		t.Fatalf("send second turn: %v", err)
	}

	h.expectType(golemclient.TypeTurnStart)
	h.expectType(golemclient.TypeTurnEnd)

	// Poll for the retried title. The scripted provider fails only the
	// first title stream; the retry must succeed and persist.
	deadline := time.Now().Add(2 * time.Second)

	for time.Now().Before(deadline) {
		summaries, err := h.store.List()
		if err == nil && len(summaries) == 1 && summaries[0].Title == "Test title" {
			break
		}

		time.Sleep(10 * time.Millisecond)
	}

	summaries, err := h.store.List()
	if err != nil {
		t.Fatalf("List: %v", err)
	}

	if len(summaries) != 1 || summaries[0].Title != "Test title" {
		t.Fatalf("summaries = %+v, want the retried title", summaries)
	}
}

func TestServer_TitleRetryFailsAgainStaysUntitled(t *testing.T) {
	t.Parallel()

	provider := &scriptedProvider{
		rounds: [][]llm.Event{
			{{Type: llm.EventTextDelta, Delta: "one"}, {Type: llm.EventUsage}},
			{{Type: llm.EventTextDelta, Delta: "two"}, {Type: llm.EventUsage}},
		},
		titleEvents: []llm.Event{{Type: llm.EventError, Err: errors.New("always down")}},
	}
	h := newHarness(t, provider)
	sess := h.createSession()

	for i, text := range []string{"first", "second"} {
		if err := h.client.Send(golemclient.UserMessage{
			Type: golemclient.TypeUserMessage, Session: sess, Text: text,
		}); err != nil {
			t.Fatalf("send turn %d: %v", i+1, err)
		}

		h.expectType(golemclient.TypeTurnStart)
		h.expectType(golemclient.TypeTurnEnd)

		time.Sleep(100 * time.Millisecond)
	}

	raw, err := os.ReadFile(filepath.Join(h.storeDir, sess+".jsonl"))
	if err != nil {
		t.Fatalf("read session file: %v", err)
	}

	if strings.Contains(string(raw), "session_title") {
		t.Errorf("permanently failing title generation must never persist a record:\n%s", raw)
	}
}
