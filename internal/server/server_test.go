package server_test

import (
	"context"
	"encoding/json"
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
// behavior (used for hang/gate scenarios).
type scriptedProvider struct {
	next     int
	rounds   [][]llm.Event
	streamFn func(ctx context.Context, round int) <-chan llm.Event
}

func (p *scriptedProvider) Stream(
	ctx context.Context,
	_ llm.Request,
) (<-chan llm.Event, error) {
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

func newHarness(t *testing.T, provider llm.Provider, approvalTimeoutS int) *harness {
	t.Helper()
	root := t.TempDir()
	storeDir := filepath.Join(t.TempDir(), "sessions")
	store, err := session.NewStore(storeDir, 8)
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	cfg := server.Config{
		Provider: provider, Model: "stub-model", Root: root,
		Store: store, ApprovalTimeoutS: approvalTimeoutS,
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
		t: t, srv: srv, client: client, sockDir: sockDir, storeDir: storeDir, root: root,
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

func TestServer_UserMessageProducesTurnLifecycle(t *testing.T) {
	t.Parallel()
	provider := &scriptedProvider{rounds: [][]llm.Event{{
		{Type: llm.EventTextDelta, Delta: "answer"},
		{Type: llm.EventUsage, InputTokens: 3, OutputTokens: 2},
	}}}
	h := newHarness(t, provider, 5)
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

func TestServer_ApprovalAllowRunsCommand(t *testing.T) {
	t.Parallel()
	provider := &scriptedProvider{rounds: [][]llm.Event{
		{{
			Type: llm.EventToolCalls,
			ToolCalls: []llm.ToolCall{{
				ID: "c1", Name: "bash",
				Args: json.RawMessage(`{"command":"echo golem-approved-marker"}`),
			}},
		}},
		{{Type: llm.EventTextDelta, Delta: "ran"}},
	}}
	h := newHarness(t, provider, 5)
	sess := h.createSession()
	if err := h.client.Send(golemclient.UserMessage{
		Type: golemclient.TypeUserMessage, Session: sess, Text: "run it",
	}); err != nil {
		t.Fatalf("send: %v", err)
	}
	req := h.expectType(golemclient.TypeApprovalRequest).(golemclient.ApprovalRequest)
	if req.CallID != "c1" || req.Tool != "bash" || !strings.Contains(req.Preview, "echo") {
		t.Fatalf("approval_request = %+v", req)
	}
	if err := h.client.Send(golemclient.ApprovalResponse{
		Type: golemclient.TypeApprovalResponse, Session: sess, Turn: req.Turn,
		ApprovalID: req.ApprovalID, Decision: "allow",
	}); err != nil {
		t.Fatalf("send approval: %v", err)
	}
	end := h.expectType(golemclient.TypeToolCallEnd).(golemclient.ToolCallEnd)
	if !end.OK || !strings.Contains(end.Result, "golem-approved-marker") {
		t.Errorf("tool_call_end = %+v", end)
	}
	h.expectType(golemclient.TypeTurnEnd)
}

func TestServer_ApprovalDenySkipsExecution(t *testing.T) {
	t.Parallel()
	provider := &scriptedProvider{rounds: [][]llm.Event{
		{{
			Type: llm.EventToolCalls,
			ToolCalls: []llm.ToolCall{{
				ID: "c1", Name: "bash",
				Args: json.RawMessage(`{"command":"echo golem-deny-marker"}`),
			}},
		}},
		{{Type: llm.EventTextDelta, Delta: "denied"}},
	}}
	h := newHarness(t, provider, 5)
	sess := h.createSession()
	if err := h.client.Send(golemclient.UserMessage{
		Type: golemclient.TypeUserMessage, Session: sess, Text: "run it",
	}); err != nil {
		t.Fatalf("send: %v", err)
	}
	req := h.expectType(golemclient.TypeApprovalRequest).(golemclient.ApprovalRequest)
	if err := h.client.Send(golemclient.ApprovalResponse{
		Type: golemclient.TypeApprovalResponse, Session: sess, Turn: req.Turn,
		ApprovalID: req.ApprovalID, Decision: "deny",
	}); err != nil {
		t.Fatalf("send denial: %v", err)
	}
	end := h.expectType(golemclient.TypeToolCallEnd).(golemclient.ToolCallEnd)
	if end.OK || strings.Contains(end.Result, "golem-deny-marker") {
		t.Errorf("denied command should not run, got %+v", end)
	}
	if !strings.Contains(end.Result, "denied") {
		t.Errorf("result should explain denial: %q", end.Result)
	}
	h.expectType(golemclient.TypeTurnEnd)
}

func TestServer_ApprovalTimeoutDefaultsDeny(t *testing.T) {
	t.Parallel()
	provider := &scriptedProvider{rounds: [][]llm.Event{
		{{
			Type: llm.EventToolCalls,
			ToolCalls: []llm.ToolCall{{
				ID: "c1", Name: "bash",
				Args: json.RawMessage(`{"command":"echo nope"}`),
			}},
		}},
		{{Type: llm.EventTextDelta, Delta: "done"}},
	}}
	h := newHarness(t, provider, 1) // 1s approval timeout
	sess := h.createSession()
	if err := h.client.Send(golemclient.UserMessage{
		Type: golemclient.TypeUserMessage, Session: sess, Text: "run",
	}); err != nil {
		t.Fatalf("send: %v", err)
	}
	h.expectType(golemclient.TypeApprovalRequest)
	// No response: wait past the timeout.
	end := h.expectType(golemclient.TypeToolCallEnd)
	toolEnd, ok := end.(golemclient.ToolCallEnd)
	if !ok || toolEnd.OK {
		t.Fatalf("timeout should deny, got %+v", end)
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
	h := newHarness(t, provider, 5)
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
	h := newHarness(t, provider, 5)
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

func TestServer_SessionListOrdersByLastActive(t *testing.T) {
	t.Parallel()
	provider := &scriptedProvider{rounds: [][]llm.Event{
		{{Type: llm.EventTextDelta, Delta: "hi"}},
	}}
	h := newHarness(t, provider, 5)
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
	h := newHarness(t, provider, 5)
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
	h := newHarness(t, provider, 5)
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
	h := newHarness(t, provider, 5)
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
