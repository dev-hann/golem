package agent_test

import (
	"strings"
	"testing"

	"github.com/dev-hann/golem/internal/agent"
	"github.com/dev-hann/golem/internal/llm"
	"github.com/dev-hann/golem/internal/tools"
)

func TestLoop_CustomSystemPromptForwarded(t *testing.T) {
	t.Parallel()
	provider := &scriptedProvider{rounds: [][]llm.Event{
		{{Type: llm.EventTextDelta, Delta: "ok"}},
	}}
	_, root := newTestAgent(t, provider)
	a := agent.New(provider, mustRegistry(t, root),
		"m", agent.WithSystemPrompt("custom instructions"))
	collect(t, a.Run(t.Context(), nil, "hi"))
	if got := provider.requests[0].SystemPrompt; got != "custom instructions" {
		t.Errorf("system prompt = %q, want custom", got)
	}
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
	_, root := newTestAgent(t, provider)
	budgeted := agent.New(provider, mustRegistry(t, root),
		"m", agent.WithBudget(20))
	writeTestFile(t, root, "big.txt", strings.Repeat("x", 5000))
	events := collect(t, budgeted.Run(t.Context(), nil, "read big"))
	end := findEvent(events, agent.EventToolCallEnd)
	if end == nil {
		t.Fatal("missing tool_call_end")
	}
	if !strings.Contains(end.Result, "[truncated]") {
		t.Errorf("result should be truncated, got %d bytes", len(end.Result))
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
