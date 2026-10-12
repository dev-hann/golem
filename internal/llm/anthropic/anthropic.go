// Package anthropic adapts the official Anthropic Go SDK to the llm.Provider
// interface. It is the reference adapter; see docs/DECISIONS.md ADR-0005.
package anthropic

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/anthropics/anthropic-sdk-go"
	"github.com/anthropics/anthropic-sdk-go/option"

	"github.com/dev-hann/golem/internal/llm"
)

// DefaultMaxTokens is the per-request output cap. Made a constant so behavior
// is reviewable in one place.
const DefaultMaxTokens = 16384

// streamBuffer sizes the normalized event channel.
const streamBuffer = 64

// toolAccumulator collects a tool_use block as its input JSON streams in.
type toolAccumulator struct {
	id   string
	name string
	args strings.Builder
}

// Provider implements llm.Provider against the Anthropic Messages API.
type Provider struct {
	client anthropic.Client
}

// NewProvider builds a provider. apiKey may be empty to fall back to
// ANTHROPIC_API_KEY; baseURL overrides the API endpoint (used by Z.ai-style
// compatible gateways and tests).
func NewProvider(apiKey, baseURL string) *Provider {
	opts := []option.RequestOption{}
	if apiKey != "" {
		opts = append(opts, option.WithAPIKey(apiKey))
	}

	if baseURL != "" {
		opts = append(opts, option.WithBaseURL(baseURL))
	}

	return &Provider{client: anthropic.NewClient(opts...)}
}

// Stream implements llm.Provider. Text deltas stream live; tool calls are
// emitted once their input JSON completes.
func (p *Provider) Stream(ctx context.Context, req llm.Request) (<-chan llm.Event, error) {
	params, err := buildParams(req)
	if err != nil {
		return nil, fmt.Errorf("anthropic: building request: %w", err)
	}

	stream := p.client.Messages.NewStreaming(ctx, params)

	out := make(chan llm.Event, streamBuffer)
	go pump(ctx, stream, out)

	return out, nil
}

// pump consumes the SDK SSE stream into normalized events.
func pump(ctx context.Context, stream interface {
	Next() bool
	Current() anthropic.MessageStreamEventUnion
	Err() error
	Close() error
}, out chan<- llm.Event,
) {
	defer close(out)
	defer func() { _ = stream.Close() }()

	tools := map[int64]*toolAccumulator{}

	var usage llm.Event
	// send prefers delivering; it gives up only when the consumer is gone
	// and the buffer is full while ctx is cancelled.
	send := makeSend(ctx, out)
	for stream.Next() {
		if handleStreamEvent(ctx, stream.Current(), send, tools, &usage) {
			emitToolCalls(tools, out)
			emitUsage(usage, out)

			return
		}
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

// handleStreamEvent processes one SSE event, accumulating tool blocks and
// usage. It returns true when the stream reached message_stop.
func handleStreamEvent(
	_ context.Context,
	ev anthropic.MessageStreamEventUnion,
	send func(llm.Event) bool,
	tools map[int64]*toolAccumulator,
	usage *llm.Event,
) bool {
	switch ev.Type { //nolint:exhaustive // SSE types not in the enum switch contract
	case "content_block_start":
		if ev.ContentBlock.Type == "tool_use" {
			block := ev.ContentBlock.AsToolUse()
			tools[ev.Index] = &toolAccumulator{id: block.ID, name: block.Name}
		}
	case "content_block_delta":
		switch ev.Delta.Type { //nolint:exhaustive // delta types outside our scope are dropped
		case "text_delta":
			send(llm.Event{Type: llm.EventTextDelta, Delta: ev.Delta.Text})
		case "input_json_delta":
			if acc, ok := tools[ev.Index]; ok {
				acc.args.WriteString(ev.Delta.PartialJSON)
			}
		default:
			// thinking/citations deltas are not forwarded in this phase.
		}
	case "message_delta":
		*usage = llm.Event{
			Type:         llm.EventUsage,
			InputTokens:  ev.Usage.InputTokens,
			OutputTokens: ev.Usage.OutputTokens,
		}
	case "message_stop":
		return true
	default:
		// message_start / content_block_stop carry nothing we need.
	}

	return false
}

// finishStream flushes leftovers after the SDK stream ended: errors first,
// then any accumulated tool calls and usage.
func finishStream(
	ctx context.Context,
	stream interface{ Err() error },
	send func(llm.Event) bool,
	tools map[int64]*toolAccumulator,
	usage llm.Event,
	out chan<- llm.Event,
) {
	if err := stream.Err(); err != nil {
		send(llm.Event{Type: llm.EventError, Err: fmt.Errorf("anthropic stream: %w", err)})
		return
	}

	if err := ctx.Err(); err != nil {
		send(llm.Event{Type: llm.EventError, Err: fmt.Errorf("anthropic stream: %w", err)})
		return
	}

	emitToolCalls(tools, out)
	emitUsage(usage, out)
}

// emitUsage sends a populated usage event on ch.
func emitUsage(e llm.Event, ch chan<- llm.Event) {
	if e.Type != llm.EventUsage {
		return
	}

	ch <- e
}

// emitToolCalls flushes accumulated tool_use blocks as one normalized event.
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
func buildParams(req llm.Request) (anthropic.MessageNewParams, error) {
	if req.Model == "" {
		return anthropic.MessageNewParams{}, fmt.Errorf("model is required")
	}

	params := anthropic.MessageNewParams{
		Model:     req.Model,
		MaxTokens: DefaultMaxTokens,
		Messages:  make([]anthropic.MessageParam, 0, len(req.Messages)),
	}
	if req.SystemPrompt != "" {
		params.System = []anthropic.TextBlockParam{{Text: req.SystemPrompt}}
	}

	for _, tool := range req.Tools {
		params.Tools = append(params.Tools, anthropic.ToolUnionParam{
			OfTool: &anthropic.ToolParam{
				Name:        tool.Name,
				Description: anthropic.String(tool.Description),
				InputSchema: anthropic.ToolInputSchemaParam{
					Properties: tool.InputSchema["properties"],
				},
			},
		})
	}

	for _, msg := range req.Messages {
		converted, err := convertMessage(msg)
		if err != nil {
			return anthropic.MessageNewParams{}, err
		}

		params.Messages = append(params.Messages, converted)
	}

	return params, nil
}

// convertMessage maps one normalized message to an SDK message param.
func convertMessage(msg llm.Message) (anthropic.MessageParam, error) {
	switch {
	case msg.Role == llm.RoleUser && len(msg.ToolResults) > 0:
		blocks := make([]anthropic.ContentBlockParamUnion, 0, len(msg.ToolResults))
		for _, r := range msg.ToolResults {
			blocks = append(blocks, anthropic.NewToolResultBlock(r.CallID, r.Output, !r.OK))
		}

		return anthropic.NewUserMessage(blocks...), nil
	case msg.Role == llm.RoleUser:
		return anthropic.NewUserMessage(anthropic.NewTextBlock(msg.Text)), nil
	case msg.Role == llm.RoleAssistant:
		return assistantMessage(msg), nil
	default:
		return anthropic.MessageParam{}, fmt.Errorf("unsupported role %q", msg.Role)
	}
}

// assistantMessage builds an assistant message from text and/or tool calls.
func assistantMessage(msg llm.Message) anthropic.MessageParam {
	blocks := make([]anthropic.ContentBlockParamUnion, 0, len(msg.ToolCalls)+1)
	if msg.Text != "" {
		blocks = append(blocks, anthropic.NewTextBlock(msg.Text))
	}

	for _, c := range msg.ToolCalls {
		blocks = append(blocks, anthropic.NewToolUseBlock(c.ID, c.Args, c.Name))
	}

	return anthropic.NewAssistantMessage(blocks...)
}
