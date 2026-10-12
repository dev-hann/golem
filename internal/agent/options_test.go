package agent_test

import (
	"strings"
	"testing"

	"github.com/dev-hann/golem/internal/agent"
	"github.com/dev-hann/golem/internal/approval"
	"github.com/dev-hann/golem/internal/llm"
	"github.com/dev-hann/golem/internal/tools"
)

func TestLoop_CustomSystemPromptForwarded(t *testing.T) {
	t.Parallel()
	provider := &scriptedProvider{rounds: [][]llm.Event{
		{{Type: llm.EventTextDelta, Delta: "ok"}},
	}}
	base, root := newTestAgent(t, provider, nil)
	a := agent.New(provider, mustRegistry(t, root), approval.NewPolicy(approval.ModePrompt, nil),
		"m", agent.WithSystemPrompt("custom instructions"))
	collect(t, a.Run(t.Context(), nil, "hi"))
	if got := provider.requests[0].SystemPrompt; got != "custom instructions" {
		t.Errorf("system prompt = %q, want custom", got)
	}
	_ = base
}

func TestLoop_BudgetTruncatesToolResults(t *testing.T) {
	t.Parallel()
	provider := &scriptedProvider{rounds: [][]llm.Event{
		{{
			Type: llm.EventToolCalls,
			ToolCalls: []llm.ToolCall{
				{ID: "1", Name: "read", Args: rawJSON(t, map[string]string{"path": "big.txt"})},
			},
		}},
		{{Type: llm.EventTextDelta, Delta: "done"}},
	}}
	a, root := newTestAgent(t, provider, nil)
	budgeted := agent.New(provider, mustRegistry(t, root),
		approval.NewPolicy(approval.ModePrompt, nil), "m", agent.WithBudget(20))
	writeTestFile(t, root, "big.txt", strings.Repeat("x", 5000))
	events := collect(t, budgeted.Run(t.Context(), nil, "read big"))
	end := findEvent(events, agent.EventToolCallEnd)
	if end == nil {
		t.Fatal("missing tool_call_end")
	}
	if !strings.Contains(end.Result, "[truncated]") {
		t.Errorf("result should be truncated, got %d bytes", len(end.Result))
	}
	_ = a
}

func TestLoop_AllAutoPolicyExecutesBash(t *testing.T) {
	t.Parallel()
	provider := &scriptedProvider{rounds: [][]llm.Event{
		{{
			Type: llm.EventToolCalls,
			ToolCalls: []llm.ToolCall{
				{ID: "1", Name: "bash", Args: rawJSON(t, map[string]string{"command": "echo ran"})},
			},
		}},
		{{Type: llm.EventTextDelta, Delta: "done"}},
	}}
	a, _ := newTestAgent(t, provider, approval.NewPolicy(approval.ModeAllAuto, nil))
	events := collect(t, a.Run(t.Context(), nil, "echo something"))
	end := findEvent(events, agent.EventToolCallEnd)
	if end == nil || !end.OK {
		t.Fatalf("bash should run under all_auto, got %+v", end)
	}
	if !strings.Contains(end.Result, "ran") {
		t.Errorf("bash output missing, got %q", end.Result)
	}
}

// mustRegistry builds the standard tool registry for agent tests.
func mustRegistry(t *testing.T, root string) *tools.Registry {
	t.Helper()
	reg, err := tools.NewRegistry(
		tools.ReadTool{Root: root},
		tools.WriteTool{Root: root},
		tools.EditTool{Root: root},
		tools.BashTool{Root: root},
		tools.GrepTool{Root: root},
		tools.GlobTool{Root: root},
	)
	if err != nil {
		t.Fatalf("NewRegistry: %v", err)
	}
	return reg
}
