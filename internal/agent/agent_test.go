package agent_test

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/dev-hann/golem/internal/agent"
	"github.com/dev-hann/golem/internal/llm"
)

// scriptedProvider streams canned rounds per Stream call and records every
// request for assertions.
type scriptedProvider struct {
	requests []llm.Request
	rounds   [][]llm.Event
	streamFn func(ctx context.Context, round int) <-chan llm.Event
}

func (p *scriptedProvider) Stream(
	ctx context.Context,
	req llm.Request,
) (<-chan llm.Event, error) {
	p.requests = append(p.requests, req)
	round := len(p.requests) - 1
	if p.streamFn != nil {
		return p.streamFn(ctx, round), nil
	}
	if round >= len(p.rounds) {
		round = len(p.rounds) - 1
	}
	out := make(chan llm.Event, len(p.rounds[round])+1)
	for _, ev := range p.rounds[round] {
		out <- ev
	}
	close(out)
	return out, nil
}

// newTestAgent wires an Agent with real tools rooted at a temp dir.
func newTestAgent(
	t *testing.T,
	provider llm.Provider,
) (a *agent.Agent, root string) {
	t.Helper()
	root = t.TempDir()
	return agent.New(provider, mustRegistry(t, root), "test-model"), root
}

// collect drains an event channel until close.
func collect(t *testing.T, events <-chan agent.Event) []agent.Event {
	t.Helper()
	var got []agent.Event
	for ev := range events {
		got = append(got, ev)
	}
	return got
}

// eventTypes summarizes events by type.
func eventTypes(events []agent.Event) []string {
	types := make([]string, 0, len(events))
	for _, ev := range events {
		types = append(types, string(ev.Type))
	}
	return types
}

func TestLoop_TextOnlyTurn_EmitsDeltasThenEnds(t *testing.T) {
	t.Parallel()
	provider := &scriptedProvider{rounds: [][]llm.Event{{
		{Type: llm.EventTextDelta, Delta: "Hello "},
		{Type: llm.EventTextDelta, Delta: "world"},
		{Type: llm.EventUsage, InputTokens: 10, OutputTokens: 5},
	}}}
	a, _ := newTestAgent(t, provider)
	events := collect(t, a.Run(t.Context(), nil, "hi"))
	want := []string{"turn_start", "text_delta", "text_delta", "turn_end"}
	got := eventTypes(events)
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Errorf("events = %v, want %v", got, want)
	}
	last := events[len(events)-1]
	if last.InputTokens != 10 || last.OutputTokens != 5 {
		t.Errorf("turn_end usage = %+v, want 10/5", last)
	}
}

func TestLoop_ToolRoundTrip_AppendsResultAndRepeats(t *testing.T) {
	t.Parallel()
	provider := &scriptedProvider{rounds: [][]llm.Event{
		{
			{Type: llm.EventTextDelta, Delta: "Reading."},
			{Type: llm.EventToolCalls, ToolCalls: []llm.ToolCall{
				{ID: "tc_1", Name: "read", Args: rawJSON(t, map[string]string{"path": "f.txt"})},
			}},
		},
		{{Type: llm.EventTextDelta, Delta: "Done."}, {Type: llm.EventUsage, InputTokens: 20, OutputTokens: 7}},
	}}
	a, root := newTestAgent(t, provider)
	writeTestFile(t, root, "f.txt", "line one\n")

	events := collect(t, a.Run(t.Context(), nil, "read f.txt"))
	if len(provider.requests) != 2 {
		t.Fatalf("provider called %d times, want 2", len(provider.requests))
	}
	second := provider.requests[1]
	if len(second.Messages) != 3 {
		t.Fatalf("second request has %d messages, want 3 (user, assistant, results)", len(second.Messages))
	}
	results := second.Messages[2].ToolResults
	if len(results) != 1 || results[0].CallID != "tc_1" || !results[0].OK {
		t.Fatalf("tool results = %+v", results)
	}
	if !strings.Contains(results[0].Output, "line one") {
		t.Errorf("result output = %q, want file content", results[0].Output)
	}
	toolEnd := findEvent(events, agent.EventToolCallEnd)
	if toolEnd == nil || !toolEnd.OK {
		t.Errorf("tool_call_end = %+v, want ok", toolEnd)
	}
}

func TestLoop_ToolError_ReturnsErrorResultToModel(t *testing.T) {
	t.Parallel()
	provider := &scriptedProvider{rounds: [][]llm.Event{
		{{
			Type: llm.EventToolCalls,
			ToolCalls: []llm.ToolCall{
				{ID: "tc_1", Name: "read", Args: rawJSON(t, map[string]string{"path": "missing.txt"})},
			},
		}},
		{{Type: llm.EventTextDelta, Delta: "It's missing."}},
	}}
	a, _ := newTestAgent(t, provider)
	events := collect(t, a.Run(t.Context(), nil, "read missing"))
	toolEnd := findEvent(events, agent.EventToolCallEnd)
	if toolEnd == nil || toolEnd.OK {
		t.Fatalf("tool_call_end should be error, got %+v", toolEnd)
	}
	if !strings.Contains(toolEnd.Result, "error:") {
		t.Errorf("error result should be marked, got: %q", toolEnd.Result)
	}
	results := provider.requests[1].Messages[2].ToolResults
	if len(results) != 1 || results[0].OK {
		t.Fatalf("second request should carry failed result, got %+v", results)
	}
}

func TestLoop_BashRunsDirectly_WithoutApprovalGate(t *testing.T) {
	t.Parallel()
	provider := &scriptedProvider{rounds: [][]llm.Event{
		{{
			Type: llm.EventToolCalls,
			ToolCalls: []llm.ToolCall{
				{ID: "tc_1", Name: "bash", Args: rawJSON(t, map[string]string{"command": "echo golem-ran"})},
			},
		}},
		{{Type: llm.EventTextDelta, Delta: "Ran, okay."}},
	}}
	a, _ := newTestAgent(t, provider)
	events := collect(t, a.Run(t.Context(), nil, "run something"))
	toolEnd := findEvent(events, agent.EventToolCallEnd)
	if toolEnd == nil || !toolEnd.OK {
		t.Fatalf("bash should run without an approval gate, got %+v", toolEnd)
	}
	if !strings.Contains(toolEnd.Result, "golem-ran") {
		t.Errorf("result should carry command output, got: %q", toolEnd.Result)
	}
}

func TestLoop_Cancel_StopsAtAwaitPoint(t *testing.T) {
	provider := &scriptedProvider{}
	provider.streamFn = func(ctx context.Context, _ int) <-chan llm.Event {
		out := make(chan llm.Event)
		go func() {
			defer close(out)
			<-ctx.Done()
			out <- llm.Event{Type: llm.EventError, Err: ctx.Err()}
		}()
		return out
	}
	a, _ := newTestAgent(t, provider)
	ctx, cancel := context.WithCancel(context.Background())
	eventsCh := a.Run(ctx, nil, "stall")
	time.Sleep(100 * time.Millisecond)
	cancel()
	var sawError bool
	for ev := range eventsCh {
		if ev.Type == agent.EventError && errors.Is(ev.Err, context.Canceled) {
			sawError = true
		}
	}
	if !sawError {
		t.Error("expected error event carrying context.Canceled")
	}
}

func TestLoop_MaxRoundsBoundsConfusedModel(t *testing.T) {
	t.Parallel()
	round := []llm.Event{{
		Type: llm.EventToolCalls,
		ToolCalls: []llm.ToolCall{
			{ID: "tc_x", Name: "glob", Args: rawJSON(t, map[string]string{"pattern": "*.txt"})},
		},
	}}
	provider := &scriptedProvider{rounds: [][]llm.Event{round}}
	a, _ := newTestAgent(t, provider)
	events := collect(t, a.Run(t.Context(), nil, "loop forever"))
	errEv := findEvent(events, agent.EventError)
	if errEv == nil || !strings.Contains(errEv.Err.Error(), "max tool rounds") {
		t.Errorf("expected max-rounds error, got %+v", errEv)
	}
}

func TestLoop_UnknownEventTypeIgnored(t *testing.T) {
	t.Parallel()
	provider := &scriptedProvider{rounds: [][]llm.Event{
		{
			{Type: llm.EventType("future_thing"), Delta: "?"},
			{Type: llm.EventTextDelta, Delta: "ok"},
		},
	}}
	a, _ := newTestAgent(t, provider)
	events := collect(t, a.Run(t.Context(), nil, "hi"))
	for _, ev := range events {
		if ev.Type == agent.EventError {
			t.Errorf("unknown provider event should not error the turn: %+v", ev)
		}
	}
	if findEvent(events, agent.EventTurnEnd) == nil {
		t.Error("turn should complete cleanly")
	}
}

// findEvent returns the first event of the given type.
func findEvent(events []agent.Event, typ agent.EventType) *agent.Event {
	for i := range events {
		if events[i].Type == typ {
			return &events[i]
		}
	}
	return nil
}

// rawJSON marshals v for tool args.
func rawJSON(t *testing.T, v any) json.RawMessage {
	t.Helper()
	data, err := json.Marshal(v)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	return data
}

// writeTestFile writes a workspace file for agent tests.
func writeTestFile(t *testing.T, root, name, content string) {
	t.Helper()
	path := filepath.Join(root, name)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("mkdir %s: %v", name, err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("write %s: %v", name, err)
	}
}
