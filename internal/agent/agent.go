// Package agent implements the golem agent loop: one user message triggers
// zero or more provider→tool rounds until the model produces a final answer.
// The loop is the only component that talks to both providers and tools.
package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/dev-hann/golem/internal/llm"
	"github.com/dev-hann/golem/internal/tools"
)

// The turn loop is unbounded: it ends when the model stops calling tools
// or the context is cancelled, not on a round cap. Rationale: a cap cut
// long legitimate tasks mid-flight with no summary (ADR-0017); cancellation
// is the guard against a confused model.

// EventType enumerates agent turn events. The names mirror the wire events in
// docs/PROTOCOL.md so the server can forward them without translation.
type EventType string

// Agent turn event types.
const (
	EventTurnStart     EventType = "turn_start"
	EventTextDelta     EventType = "text_delta"
	EventThinkingDelta EventType = "thinking_delta"
	EventRetry         EventType = "retry"
	EventToolCallStart EventType = "tool_call_start"
	EventToolCallEnd   EventType = "tool_call_end"
	EventError         EventType = "error"
	EventTurnEnd       EventType = "turn_end"
)

// Event is one observable step of a turn.
type Event struct {
	Type         EventType
	Delta        string
	CallID       string
	Tool         string
	Args         json.RawMessage
	OK           bool
	Result       string
	InputTokens  int64
	OutputTokens int64
	// Attempt/Retries/DelayMs carry the retry wrapper's announcement of an
	// upcoming provider retry (EventRetry).
	Attempt int
	Retries int
	DelayMs int
	Err     error
}

// Agent drives provider/tool rounds for a conversation.
type Agent struct {
	provider llm.Provider
	registry *tools.Registry
	model    string
	system   string
	// extraInstructions is discovered AGENTS.md content appended after
	// system on every request (ADR-0019).
	extraInstructions string
	budget            int
}

// Option customizes an Agent.
type Option func(*Agent)

// WithSystemPrompt overrides the built-in system prompt.
func WithSystemPrompt(prompt string) Option {
	return func(a *Agent) { a.system = prompt }
}

// WithBudget overrides the tool-result byte budget.
func WithBudget(bytes int) Option {
	return func(a *Agent) { a.budget = bytes }
}

// WithInstructions appends discovered AGENTS.md content after the system
// prompt (global file first, project file last — ADR-0019). Empty strings
// are ignored.
func WithInstructions(instructions string) Option {
	return func(a *Agent) {
		if strings.TrimSpace(instructions) == "" {
			return
		}

		a.extraInstructions = instructions
	}
}

// defaultSystemPrompt is the built-in instruction set for the model.
const defaultSystemPrompt = `You are golem, a coding agent working in the user's workspace.
Use the provided tools to read, search, edit files and run commands.
Prefer the edit tool over rewriting whole files.
Verify your changes (run tests when available) before claiming completion.
Be concise.`

// joinedSystemPrompt composes the built-in (or overridden) system prompt
// with discovered AGENTS.md instructions: base first, instructions after,
// so project rules read as later — more specific — guidance.
func (a *Agent) joinedSystemPrompt() string {
	if strings.TrimSpace(a.extraInstructions) == "" {
		return a.system
	}

	return a.system + "\n\n" + a.extraInstructions
}

// New builds an Agent. model selects the provider model; registry supplies
// the tool implementations.
func New(
	provider llm.Provider,
	registry *tools.Registry,
	model string,
	opts ...Option,
) *Agent {
	a := &Agent{
		provider: provider,
		registry: registry,
		model:    model,
		system:   defaultSystemPrompt,
		budget:   tools.DefaultOutputBudget,
	}
	for _, opt := range opts {
		opt(a)
	}

	return a
}

// Run executes one turn: it appends userText to history, streams provider
// events, dispatches tool calls, feeds results back, and repeats until the
// model stops calling tools. The returned channel closes when the turn is
// complete or fatally errored.
func (a *Agent) Run(ctx context.Context, history []llm.Message, userText string) <-chan Event {
	out := make(chan Event, eventBuffer)
	go func() {
		defer close(out)

		messages := make([]llm.Message, len(history), len(history)+2)
		copy(messages, history)
		messages = append(messages, llm.Message{Role: llm.RoleUser, Text: userText})
		a.runTurn(ctx, messages, out)
	}()

	return out
}

// eventBuffer sizes the event channel; enough to keep a fast provider from
// blocking the loop head-of-line.
const eventBuffer = 64

// runTurn is the round loop. It emits events via emit (which respects
// cancellation) and returns on fatal error, cancellation, or completion.
func (a *Agent) runTurn(ctx context.Context, messages []llm.Message, out chan<- Event) {
	var inTok, outTok int64
	// emit prefers delivering the event; it only gives up when the consumer
	// is gone and the buffer is full while ctx is cancelled.
	emit := func(ev Event) bool {
		select {
		case out <- ev:
			return true
		default:
		}

		select {
		case out <- ev:
			return true
		case <-ctx.Done():
			return false
		}
	}
	if !emit(Event{Type: EventTurnStart}) {
		return
	}

	for {
		text, calls, usage, fatal := a.streamRound(ctx, messages, emit)
		inTok += usage.input
		outTok += usage.output

		if fatal != nil {
			emit(Event{Type: EventError, Err: fatal})
			return
		}

		if len(calls) == 0 {
			emit(Event{Type: EventTurnEnd, InputTokens: inTok, OutputTokens: outTok})
			return
		}

		messages = append(messages, llm.Message{Role: llm.RoleAssistant, Text: text, ToolCalls: calls})

		results, ok := a.executeCalls(ctx, calls, emit)
		if !ok {
			return
		}

		messages = append(messages, llm.Message{Role: llm.RoleUser, ToolResults: results})
	}
}

// roundUsage accumulates per-round token counts.
type roundUsage struct {
	input  int64
	output int64
}

// streamRound consumes one provider stream, forwarding text deltas and
// collecting tool calls. Returns the assistant text, tool calls, usage, and a
// fatal error (provider failure or cancellation), if any.
func (a *Agent) streamRound(
	ctx context.Context,
	messages []llm.Message,
	emit func(Event) bool,
) (string, []llm.ToolCall, roundUsage, error) {
	req := llm.Request{
		Model:        a.model,
		SystemPrompt: a.joinedSystemPrompt(),
		Messages:     messages,
		Tools:        a.registry.Specs(),
	}

	stream, err := a.provider.Stream(ctx, req)
	if err != nil {
		return "", nil, roundUsage{}, fmt.Errorf("starting provider stream: %w", err)
	}

	var (
		text  string
		calls []llm.ToolCall
	)

	usage := roundUsage{}

	for event := range stream {
		switch event.Type {
		case llm.EventTextDelta:
			text += event.Delta
			emit(Event{Type: EventTextDelta, Delta: event.Delta})
		case llm.EventThinkingDelta:
			// Reasoning content never joins the conversation; the event
			// exists so clients can show a reasoning state.
			emit(Event{Type: EventThinkingDelta})
		case llm.EventRetry:
			emit(Event{
				Type: EventRetry, Attempt: event.Attempt,
				Retries: event.Retries, DelayMs: event.DelayMs,
			})
		case llm.EventToolCalls:
			calls = append(calls, event.ToolCalls...)
		case llm.EventUsage:
			usage.input += event.InputTokens
			usage.output += event.OutputTokens
		case llm.EventError:
			return text, calls, usage, event.Err
		default:
			// Unknown normalized events are ignored per PROTOCOL.md.
		}
	}

	if err := ctx.Err(); err != nil {
		return text, calls, usage, fmt.Errorf("turn cancelled: %w", err)
	}

	return text, calls, usage, nil
}

// executeCalls runs each tool call through the registry, emitting start/end
// events and returning the results to append as the next user message.
func (a *Agent) executeCalls(
	ctx context.Context,
	calls []llm.ToolCall,
	emit func(Event) bool,
) ([]llm.ToolResult, bool) {
	results := make([]llm.ToolResult, 0, len(calls))
	for _, call := range calls {
		if ctx.Err() != nil {
			return nil, false
		}

		emit(Event{Type: EventToolCallStart, CallID: call.ID, Tool: call.Name, Args: call.Args})
		result := a.runOne(ctx, call)
		emit(Event{
			Type:   EventToolCallEnd,
			CallID: call.ID,
			Tool:   call.Name,
			OK:     result.OK,
			Result: result.Output,
		})
		results = append(results, result)
	}

	return results, true
}

// runOne executes a single call.
func (a *Agent) runOne(ctx context.Context, call llm.ToolCall) llm.ToolResult {
	result := llm.ToolResult{CallID: call.ID, Name: call.Name, OK: true}

	output, err := a.registry.Execute(ctx, call.Name, call.Args)
	if err != nil {
		result.OK = false
		output = "error: " + err.Error()
	}

	result.Output, _ = tools.Truncate(output, a.budget)

	return result
}
