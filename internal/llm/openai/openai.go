// Package openai adapts the official OpenAI Go SDK to the llm.Provider
// interface. One adapter serves every OpenAI-compatible endpoint — openai,
// zai, ollama, and gateways — via the base URL override (ADR-0005).
package openai

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/openai/openai-go"
	"github.com/openai/openai-go/option"
	"github.com/openai/openai-go/shared"

	"github.com/dev-hann/golem/internal/llm"
)

// DefaultMaxTokens is the per-request output cap, mirroring the anthropic
// adapter so behavior is reviewable in one place.
const DefaultMaxTokens = 16384

// streamBuffer sizes the normalized event channel.
const streamBuffer = 64

// toolAccumulator collects one streamed function call.
type toolAccumulator struct {
	id   string
	name string
	args strings.Builder
}

// Provider implements llm.Provider against the OpenAI chat-completions API
// and its compatible endpoints.
type Provider struct {
	client openai.Client
}

// NewProvider builds a provider. apiKey may be empty for keyless endpoints
// (ollama); baseURL overrides the endpoint (zai, gateways, tests).
func NewProvider(apiKey, baseURL string) *Provider {
	opts := []option.RequestOption{}
	if apiKey != "" {
		opts = append(opts, option.WithAPIKey(apiKey))
	}

	if baseURL != "" {
		opts = append(opts, option.WithBaseURL(baseURL))
	}

	return &Provider{client: openai.NewClient(opts...)}
}

// Stream implements llm.Provider. Text deltas stream live; tool calls are
// emitted once their streamed argument JSON completes.
func (p *Provider) Stream(ctx context.Context, req llm.Request) (<-chan llm.Event, error) {
	params, err := buildParams(req)
	if err != nil {
		return nil, fmt.Errorf("openai: building request: %w", err)
	}

	stream := p.client.Chat.Completions.NewStreaming(ctx, params)

	out := make(chan llm.Event, streamBuffer)
	go pump(ctx, stream, out)

	return out, nil
}

// chunkStream is the SDK SSE stream surface pump depends on.
type chunkStream interface {
	Next() bool
	Current() openai.ChatCompletionChunk
	Err() error
	Close() error
}

// pump consumes the SDK SSE stream into normalized events.
func pump(ctx context.Context, stream chunkStream, out chan<- llm.Event) {
	defer close(out)
	defer func() { _ = stream.Close() }()

	tools := map[int64]*toolAccumulator{}

	var usage llm.Event

	send := makeSend(ctx, out)

	for stream.Next() {
		handleChunk(stream.Current(), send, tools, &usage)
	}

	finishStream(ctx, stream, send, tools, usage, out)
}

// makeSend builds an event sender that prefers delivery and only gives up
// when the consumer is gone and the buffer is full while ctx is cancelled.
func makeSend(ctx context.Context, out chan<- llm.Event) func(llm.Event) bool {
	return func(ev llm.Event) bool {
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
}

// handleChunk processes one SSE chunk: text deltas stream out immediately,
// tool-call fragments accumulate by index, usage lands on the terminal
// chunk (include_usage).
func handleChunk(
	chunk openai.ChatCompletionChunk,
	send func(llm.Event) bool,
	tools map[int64]*toolAccumulator,
	usage *llm.Event,
) {
	for _, choice := range chunk.Choices {
		if choice.Delta.Content != "" {
			send(llm.Event{Type: llm.EventTextDelta, Delta: choice.Delta.Content})
		}

		for _, tc := range choice.Delta.ToolCalls {
			acc := tools[tc.Index]
			if acc == nil {
				acc = &toolAccumulator{}
				tools[tc.Index] = acc
			}

			if tc.ID != "" {
				acc.id = tc.ID
			}

			if tc.Function.Name != "" {
				acc.name = tc.Function.Name
			}

			acc.args.WriteString(tc.Function.Arguments)
		}
	}

	if chunk.Usage.TotalTokens > 0 {
		*usage = llm.Event{
			Type:         llm.EventUsage,
			InputTokens:  chunk.Usage.PromptTokens,
			OutputTokens: chunk.Usage.CompletionTokens,
		}
	}
}

// finishStream flushes leftovers after the SDK stream ended: errors first,
// then any accumulated tool calls and usage.
func finishStream(
	ctx context.Context,
	stream chunkStream,
	send func(llm.Event) bool,
	tools map[int64]*toolAccumulator,
	usage llm.Event,
	out chan<- llm.Event,
) {
	if err := stream.Err(); err != nil {
		send(llm.Event{Type: llm.EventError, Err: fmt.Errorf("openai stream: %w", classifyStreamError(err))})

		return
	}

	if err := ctx.Err(); err != nil {
		send(llm.Event{Type: llm.EventError, Err: fmt.Errorf("openai stream: %w", err)})

		return
	}

	emitToolCalls(tools, out)
	emitUsage(usage, out)
}

// classifyStreamError maps SDK failures onto retry markers: rate limits,
// 5xx, and status-less network drops are transient; 401/403 are auth; the
// rest passes plain. The 401/403 constants are shared with listmodels.go.
func classifyStreamError(err error) error {
	var apiErr *openai.Error

	switch {
	case errors.As(err, &apiErr) && (apiErr.StatusCode == httpTooManyRequests || apiErr.StatusCode >= 500):
		return llm.MarkTransient(err)
	case errors.As(err, &apiErr) &&
		(apiErr.StatusCode == httpUnauthorized || apiErr.StatusCode == httpForbidden):
		return llm.MarkAuth(err)
	case errors.As(err, &apiErr):
		return err
	default:
		// No HTTP status: connection reset, EOF, DNS — retryable.
		return llm.MarkTransient(err)
	}
}

// HTTP status used for stream classification; 401/403 live in listmodels.go.
const httpTooManyRequests = 429

// emitUsage sends a populated usage event on ch.
func emitUsage(e llm.Event, ch chan<- llm.Event) {
	if e.Type != llm.EventUsage {
		return
	}

	ch <- e
}

// emitToolCalls flushes accumulated function calls as one normalized event.
func emitToolCalls(tools map[int64]*toolAccumulator, out chan<- llm.Event) {
	if len(tools) == 0 {
		return
	}

	calls := make([]llm.ToolCall, 0, len(tools))
	for _, acc := range tools {
		args := json.RawMessage(acc.args.String())
		if len(args) == 0 {
			args = json.RawMessage("{}")
		}

		calls = append(calls, llm.ToolCall{ID: acc.id, Name: acc.name, Args: args})
	}

	out <- llm.Event{Type: llm.EventToolCalls, ToolCalls: calls}
}

// buildParams converts a normalized request into SDK params.
func buildParams(req llm.Request) (openai.ChatCompletionNewParams, error) {
	if req.Model == "" {
		return openai.ChatCompletionNewParams{}, fmt.Errorf("model is required")
	}

	params := openai.ChatCompletionNewParams{
		Model:         req.Model,
		MaxTokens:     openai.Int(DefaultMaxTokens),
		StreamOptions: openai.ChatCompletionStreamOptionsParam{IncludeUsage: openai.Bool(true)},
		Messages:      make([]openai.ChatCompletionMessageParamUnion, 0, len(req.Messages)),
	}

	if req.SystemPrompt != "" {
		params.Messages = append(params.Messages, openai.SystemMessage(req.SystemPrompt))
	}

	for _, tool := range req.Tools {
		params.Tools = append(params.Tools, openai.ChatCompletionToolParam{
			Function: shared.FunctionDefinitionParam{
				Name:        tool.Name,
				Description: openai.String(tool.Description),
				Parameters:  shared.FunctionParameters(tool.InputSchema),
			},
		})
	}

	for _, msg := range req.Messages {
		params.Messages = appendMessages(params.Messages, msg)
	}

	return params, nil
}

// appendMessages maps one normalized message onto zero or more OpenAI
// messages: each tool result becomes its own tool-role message.
func appendMessages(
	dst []openai.ChatCompletionMessageParamUnion,
	msg llm.Message,
) []openai.ChatCompletionMessageParamUnion {
	switch {
	case msg.Role == llm.RoleUser && len(msg.ToolResults) > 0:
		for _, r := range msg.ToolResults {
			content := r.Output
			if !r.OK {
				content += "\n(tool error)"
			}

			dst = append(dst, openai.ToolMessage(content, r.CallID))
		}
	case msg.Role == llm.RoleUser:
		dst = append(dst, openai.UserMessage(msg.Text))
	case msg.Role == llm.RoleAssistant:
		dst = append(dst, assistantMessage(msg))
	}

	return dst
}

// assistantMessage builds an assistant message from text and/or tool calls.
func assistantMessage(msg llm.Message) openai.ChatCompletionMessageParamUnion {
	assistant := openai.ChatCompletionAssistantMessageParam{}
	if msg.Text != "" {
		assistant.Content = openai.ChatCompletionAssistantMessageParamContentUnion{
			OfString: openai.String(msg.Text),
		}
	}

	for _, c := range msg.ToolCalls {
		assistant.ToolCalls = append(assistant.ToolCalls, openai.ChatCompletionMessageToolCallParam{
			ID: c.ID,
			Function: openai.ChatCompletionMessageToolCallFunctionParam{
				Name:      c.Name,
				Arguments: string(c.Args),
			},
		})
	}

	return openai.ChatCompletionMessageParamUnion{OfAssistant: &assistant}
}
