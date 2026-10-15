package agent_test

import (
	"strings"
	"testing"

	"github.com/dev-hann/golem/internal/agent"
	"github.com/dev-hann/golem/internal/llm"
)

// ADR-0031: the assembled prompt is base → environment → AGENTS.md, so
// project instructions still read last (position-based precedence).

func TestLoop_EnvironmentPrecedesInstructions(t *testing.T) {
	t.Parallel()

	provider := &scriptedProvider{rounds: [][]llm.Event{
		{{Type: llm.EventTextDelta, Delta: "ok"}},
	}}
	_, root := newTestAgent(t, provider)
	a := agent.New(provider, mustRegistry(t, root), "m",
		agent.WithEnvironment("ENV-MARKER"),
		agent.WithInstructions("AGENTS-MARKER"))

	collect(t, a.Run(t.Context(), nil, "hi"))

	got := provider.requests[0].SystemPrompt
	envAt := strings.Index(got, "ENV-MARKER")
	agentsAt := strings.Index(got, "AGENTS-MARKER")
	if envAt < 0 || agentsAt < 0 {
		t.Fatalf("markers missing in prompt:\n%s", got)
	}
	if envAt > agentsAt {
		t.Errorf("environment must precede AGENTS.md content:\n%s", got)
	}
}

func TestLoop_EnvironmentAloneStillAppends(t *testing.T) {
	t.Parallel()

	provider := &scriptedProvider{rounds: [][]llm.Event{
		{{Type: llm.EventTextDelta, Delta: "ok"}},
	}}
	_, root := newTestAgent(t, provider)
	a := agent.New(provider, mustRegistry(t, root), "m",
		agent.WithEnvironment("ENV-MARKER"))

	collect(t, a.Run(t.Context(), nil, "hi"))

	got := provider.requests[0].SystemPrompt
	if !strings.Contains(got, "ENV-MARKER") {
		t.Errorf("environment missing from prompt:\n%s", got)
	}
	if !strings.Contains(got, "You are golem") {
		t.Errorf("environment-only prompt lost the base:\n%s", got)
	}
}

func TestLoop_EnvironmentBlankIsNoOp(t *testing.T) {
	t.Parallel()

	run := func(opts ...agent.Option) string {
		t.Helper()
		provider := &scriptedProvider{rounds: [][]llm.Event{
			{{Type: llm.EventTextDelta, Delta: "ok"}},
		}}
		_, root := newTestAgent(t, provider)
		a := agent.New(provider, mustRegistry(t, root), "m", opts...)
		collect(t, a.Run(t.Context(), nil, "hi"))
		return provider.requests[0].SystemPrompt
	}

	if withBlank, plain := run(agent.WithEnvironment(" \n ")), run(); withBlank != plain {
		t.Errorf("blank environment changed the prompt:\nwith: %q\nplain: %q",
			withBlank, plain)
	}
}
