// Package llm defines the provider-agnostic interface and types used by the
// agent loop. Adapters in subpackages normalize provider SDKs into this shape.
package llm

import (
	"context"
	"encoding/json"
	"errors"
)

// Role identifies the author of a Message.
type Role string

// Message roles.
const (
	RoleUser      Role = "user"
	RoleAssistant Role = "assistant"
)

// ToolSpec describes a tool to the model provider.
type ToolSpec struct {
	Name        string
	Description string
	InputSchema map[string]any
}

// ToolCall is a model-requested tool invocation.
type ToolCall struct {
	ID   string
	Name string
	Args json.RawMessage
}

// ToolResult carries the outcome of a ToolCall back into the conversation.
type ToolResult struct {
	CallID string
	Name   string
	OK     bool
	Output string
}

// Message is one conversational item: plain text, tool calls, or tool results.
// A message carries at most one kind of payload; which one is set depends on
// Role and on what the provider emitted for that step.
type Message struct {
	Role        Role
	Text        string
	ToolCalls   []ToolCall
	ToolResults []ToolResult
}

// Request is a provider completion request.
type Request struct {
	Model        string
	SystemPrompt string
	Messages     []Message
	Tools        []ToolSpec
}

// EventType enumerates normalized provider stream event kinds.
type EventType string

// Provider stream event types.
const (
	EventTextDelta     EventType = "text_delta"
	EventThinkingDelta EventType = "thinking_delta"
	EventRetry         EventType = "retry"
	EventToolCalls     EventType = "tool_calls"
	EventUsage         EventType = "usage"
	EventError         EventType = "error"
)

// Sentinel errors adapters wrap so callers can classify failures without
// matching on SDK types.
var (
	// ErrAuth marks a rejected credential (HTTP 401/403).
	ErrAuth = errors.New("credential rejected")
	// ErrModelListUnavailable marks a provider whose model-list endpoint is
	// missing or returned nothing — the key may still be fine.
	ErrModelListUnavailable = errors.New("model list unavailable")
	// ErrEmptyReasoningRound marks a stream that ended truncated
	// (finish_reason=length) without producing any text or tool call —
	// typically a reasoning model that spent the whole output budget on
	// invisible thinking.
	ErrEmptyReasoningRound = errors.New(
		"empty reasoning round: model hit the max-token limit before writing anything" +
			" (raise max_tokens or disable thinking)")
)

// Event is a normalized provider stream event.
type Event struct {
	Type EventType
	// Delta carries one text (EventTextDelta) or extended-thinking
	// (EventThinkingDelta) chunk.
	Delta string
	// Attempt/Retries/DelayMs describe an upcoming provider retry
	// (EventRetry): which retry runs, the policy's cap, and the backoff
	// the wrapper is about to sleep.
	Attempt      int
	Retries      int
	DelayMs      int
	ToolCalls    []ToolCall
	InputTokens  int64
	OutputTokens int64
	Err          error
}

// Provider streams completions from a model provider. Implementations must
// respect ctx cancellation by closing the returned channel after emitting an
// EventError carrying the context error.
type Provider interface {
	Stream(ctx context.Context, req Request) (<-chan Event, error)
}
