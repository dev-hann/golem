package openai_test

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/dev-hann/golem/internal/llm"
	openaicompat "github.com/dev-hann/golem/internal/llm/openai"
)

// chunk is one OpenAI chat.completion.chunk SSE payload.
type chunk map[string]any

// sseServer serves the given chunks as a chat-completions stream and fails
// the test if no request ever arrives.
func sseServer(t *testing.T, status int, chunks []chunk) *httptest.Server {
	t.Helper()

	var gotRequest bool

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		gotRequest = true

		w.Header().Set("Content-Type", "text/event-stream")
		if status != http.StatusOK {
			w.WriteHeader(status)
			fmt.Fprintf(w, `{"error":{"message":"bad key","type":"invalid_request_error"}}`) //nolint:errcheck // test stub

			return
		}

		flusher := w.(http.Flusher)
		for _, c := range chunks {
			data, err := json.Marshal(c)
			if err != nil {
				t.Errorf("marshal chunk: %v", err)

				return
			}

			fmt.Fprintf(w, "data: %s\n\n", data) //nolint:errcheck // test stub
			flusher.Flush()
		}

		fmt.Fprint(w, "data: [DONE]\n\n") //nolint:errcheck // test stub
		flusher.Flush()
	}))
	t.Cleanup(srv.Close)

	t.Cleanup(func() {
		t.Helper()

		if !gotRequest {
			t.Errorf("stub server never received a request")
		}
	})

	return srv
}

// baseChunk carries the fields the SDK marks required.
func baseChunk() chunk {
	return chunk{
		"id": "chatcmpl-1", "object": "chat.completion.chunk", "created": 0, "model": "stub-model",
	}
}

// withChoice returns a copy of the base chunk with one choice appended.
func withChoice(delta map[string]any, finish any) chunk {
	c := baseChunk()
	c["choices"] = []any{map[string]any{"index": 0, "delta": delta, "finish_reason": finish}}

	return c
}

// usageChunk is the terminal usage-bearing chunk (empty choices).
func usageChunk() chunk {
	c := baseChunk()
	c["choices"] = []any{}
	c["usage"] = map[string]any{"prompt_tokens": 11, "completion_tokens": 7, "total_tokens": 18}

	return c
}

// collect drains a stream into a slice, failing on unexpected latency.
func collect(t *testing.T, events <-chan llm.Event) []llm.Event {
	t.Helper()

	var got []llm.Event

	for {
		select {
		case ev, ok := <-events:
			if !ok {
				return got
			}
			got = append(got, ev)
		case <-time.After(5 * time.Second):
			t.Fatal("stream did not close within 5s")
		}
	}
}

// stream opens a provider stream against the stub.
func stream(t *testing.T, baseURL string, msgs []llm.Message, tools []llm.ToolSpec) <-chan llm.Event {
	t.Helper()

	provider := openaicompat.NewProvider("test-key", baseURL)
	out, err := provider.Stream(t.Context(), llm.Request{
		Model: "stub-model", Messages: msgs, Tools: tools,
	})
	if err != nil {
		t.Fatalf("Stream: %v", err)
	}

	return out
}

func TestStream_NormalizesTextDeltasAndUsage(t *testing.T) {
	t.Parallel()

	srv := sseServer(t, http.StatusOK, []chunk{
		withChoice(map[string]any{"role": "assistant"}, nil),
		withChoice(map[string]any{"content": "Hel"}, nil),
		withChoice(map[string]any{"content": "lo"}, nil),
		withChoice(map[string]any{}, "stop"),
		usageChunk(),
	})

	got := collect(t, stream(t, srv.URL, []llm.Message{{Role: llm.RoleUser, Text: "hi"}}, nil))

	var text strings.Builder
	var usage llm.Event

	for _, ev := range got {
		switch ev.Type {
		case llm.EventTextDelta:
			text.WriteString(ev.Delta)
		case llm.EventUsage:
			usage = ev
		case llm.EventError:
			t.Fatalf("unexpected error event: %v", ev.Err)
		case llm.EventToolCalls:
			t.Fatalf("unexpected tool calls in text test: %+v", ev.ToolCalls)
		case llm.EventThinkingDelta, llm.EventRetry:
			// reasoning and retry noise: not asserted here
		}
	}

	if text.String() != "Hello" {
		t.Errorf("text = %q, want Hello", text.String())
	}
	if usage.InputTokens != 11 || usage.OutputTokens != 7 {
		t.Errorf("usage = %+v, want in=11 out=7", usage)
	}
}

func TestStream_AccumulatesStreamedToolCalls(t *testing.T) {
	t.Parallel()

	srv := sseServer(t, http.StatusOK, []chunk{
		withChoice(map[string]any{
			"role": "assistant",
			"tool_calls": []any{map[string]any{
				"index": 0, "id": "call_1", "type": "function",
				"function": map[string]any{"name": "read", "arguments": `{"pa`},
			}},
		}, nil),
		withChoice(map[string]any{
			"tool_calls": []any{map[string]any{
				"index":    0,
				"function": map[string]any{"arguments": `th":"x"}`},
			}},
		}, nil),
		withChoice(map[string]any{}, "tool_calls"),
		usageChunk(),
	})

	got := collect(t, stream(t, srv.URL, []llm.Message{{Role: llm.RoleUser, Text: "read x"}}, nil))

	var calls []llm.ToolCall

	for _, ev := range got {
		if ev.Type == llm.EventToolCalls {
			calls = ev.ToolCalls
		}
		if ev.Type == llm.EventError {
			t.Fatalf("unexpected error event: %v", ev.Err)
		}
	}

	if len(calls) != 1 {
		t.Fatalf("tool calls = %+v, want 1", calls)
	}
	if calls[0].ID != "call_1" || calls[0].Name != "read" {
		t.Errorf("call = %+v", calls[0])
	}
	if string(calls[0].Args) != `{"path":"x"}` {
		t.Errorf("args = %s, want %s", calls[0].Args, `{"path":"x"}`)
	}
}

func TestStream_SendsConversationAndToolsUpstream(t *testing.T) {
	t.Parallel()

	var request struct {
		Messages []struct {
			Role       string `json:"role"`
			Content    string `json:"content"`
			ToolCallID string `json:"tool_call_id"`
			ToolCalls  []struct {
				ID       string `json:"id"`
				Function struct {
					Name      string `json:"name"`
					Arguments string `json:"arguments"`
				} `json:"function"`
			} `json:"tool_calls"`
		} `json:"messages"`
		Tools []struct {
			Function struct {
				Name string `json:"name"`
			} `json:"function"`
		} `json:"tools"`
	}

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		if err := json.Unmarshal(body, &request); err != nil {
			t.Errorf("unmarshal request: %v", err)
		}

		w.Header().Set("Content-Type", "text/event-stream")
		flusher := w.(http.Flusher)
		fmt.Fprintf(w, "data: %s\n\n", func() string { //nolint:errcheck // test stub
			b, _ := json.Marshal(withChoice(map[string]any{}, "stop"))
			return string(b)
		}())
		fmt.Fprint(w, "data: [DONE]\n\n") //nolint:errcheck // test stub
		flusher.Flush()
	}))
	t.Cleanup(srv.Close)

	msgs := []llm.Message{
		{Role: llm.RoleUser, Text: "read x"},
		{Role: llm.RoleAssistant, ToolCalls: []llm.ToolCall{
			{ID: "call_1", Name: "read", Args: json.RawMessage(`{"path":"x"}`)},
		}},
		{Role: llm.RoleUser, ToolResults: []llm.ToolResult{
			{CallID: "call_1", Name: "read", OK: true, Output: "contents"},
		}},
	}
	tools := []llm.ToolSpec{{
		Name: "read", Description: "read a file",
		InputSchema: map[string]any{"type": "object", "properties": map[string]any{}},
	}}

	collect(t, stream(t, srv.URL, msgs, tools))

	if len(request.Tools) != 1 || request.Tools[0].Function.Name != "read" {
		t.Errorf("tools sent = %+v", request.Tools)
	}
	if len(request.Messages) != 3 {
		t.Fatalf("messages sent = %+v, want 3", request.Messages)
	}

	assistant := request.Messages[1]
	if assistant.Role != "assistant" || len(assistant.ToolCalls) != 1 ||
		assistant.ToolCalls[0].ID != "call_1" || assistant.ToolCalls[0].Function.Name != "read" {
		t.Errorf("assistant message = %+v", assistant)
	}

	toolMsg := request.Messages[2]
	if toolMsg.Role != "tool" || toolMsg.ToolCallID != "call_1" || toolMsg.Content != "contents" {
		t.Errorf("tool result message = %+v", toolMsg)
	}
}

func TestStream_NormalizesReasoningContentToThinkingDeltas(t *testing.T) {
	t.Parallel()

	// GLM (zai) streams chain-of-thought in the non-standard
	// delta.reasoning_content field, which the SDK drops on the floor
	// (it is absent from ChatCompletionChunkChoiceDelta). Without
	// normalization the reasoning budget burns invisibly and the client
	// sees a frozen turn.
	srv := sseServer(t, http.StatusOK, []chunk{
		withChoice(map[string]any{
			"role": "assistant", "reasoning_content": "thinking hard",
		}, nil),
		withChoice(map[string]any{"content": "answer"}, nil),
		withChoice(map[string]any{}, "stop"),
		usageChunk(),
	})

	got := collect(t, stream(t, srv.URL, []llm.Message{{Role: llm.RoleUser, Text: "hi"}}, nil))

	var thinking strings.Builder
	var text strings.Builder

	for _, ev := range got {
		switch ev.Type {
		case llm.EventThinkingDelta:
			thinking.WriteString(ev.Delta)
		case llm.EventTextDelta:
			text.WriteString(ev.Delta)
		case llm.EventError:
			t.Fatalf("unexpected error event: %v", ev.Err)
		case llm.EventToolCalls, llm.EventUsage, llm.EventRetry:
			// not asserted here
		}
	}

	if thinking.String() != "thinking hard" {
		t.Errorf("thinking deltas = %q, want %q", thinking.String(), "thinking hard")
	}
	if text.String() != "answer" {
		t.Errorf("text = %q, want answer", text.String())
	}
}

func TestStream_LengthFinishWithoutContent_SurfacesError(t *testing.T) {
	t.Parallel()

	// A reasoning-heavy model can spend the whole max_tokens budget on
	// invisible reasoning: finish_reason=length with zero content and
	// zero tool calls. Silently ending the round there produces turns
	// that look dead — an explicit error must surface instead.
	srv := sseServer(t, http.StatusOK, []chunk{
		withChoice(map[string]any{"reasoning_content": "burn"}, nil),
		withChoice(map[string]any{}, "length"),
		usageChunk(),
	})

	got := collect(t, stream(t, srv.URL, []llm.Message{{Role: llm.RoleUser, Text: "hi"}}, nil))

	var errEvent *llm.Event

	for i, ev := range got {
		if ev.Type == llm.EventError {
			errEvent = &got[i]
		}
	}

	if errEvent == nil {
		t.Fatalf("events = %+v, want an error event", got)
	}
	if !strings.Contains(errEvent.Err.Error(), "max-token") {
		t.Errorf("error = %v, want max-token truncation mention", errEvent.Err)
	}
}

func TestStream_LengthFinishAfterContent_StillCompletes(t *testing.T) {
	t.Parallel()

	// finish_reason=length with streamed content is truncation the user
	// can see; the stream must complete normally, not error.
	srv := sseServer(t, http.StatusOK, []chunk{
		withChoice(map[string]any{"content": "partial answer"}, nil),
		withChoice(map[string]any{}, "length"),
		usageChunk(),
	})

	got := collect(t, stream(t, srv.URL, []llm.Message{{Role: llm.RoleUser, Text: "hi"}}, nil))

	var text strings.Builder
	errCount := 0

	for _, ev := range got {
		switch ev.Type {
		case llm.EventTextDelta:
			text.WriteString(ev.Delta)
		case llm.EventError:
			errCount++
		case llm.EventThinkingDelta, llm.EventToolCalls, llm.EventUsage, llm.EventRetry:
			// not asserted here
		}
	}

	if text.String() != "partial answer" {
		t.Errorf("text = %q, want partial answer", text.String())
	}
	if errCount != 0 {
		t.Errorf("truncation with visible content should not error, got %d error events", errCount)
	}
}

func TestStream_SurfacesAPIError(t *testing.T) {
	t.Parallel()

	srv := sseServer(t, http.StatusUnauthorized, nil)

	got := collect(t, stream(t, srv.URL, []llm.Message{{Role: llm.RoleUser, Text: "hi"}}, nil))

	if len(got) != 1 || got[0].Type != llm.EventError {
		t.Fatalf("events = %+v, want single error", got)
	}
	if !strings.Contains(got[0].Err.Error(), "401") {
		t.Errorf("error = %v, want status mention", got[0].Err)
	}
}

func TestStream_ContextCancelClosesChannel(t *testing.T) {
	t.Parallel()

	release := make(chan struct{})

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, "data: ") //nolint:errcheck // test stub
		w.(http.Flusher).Flush()
		<-release
	}))
	t.Cleanup(srv.Close)
	t.Cleanup(func() { close(release) })

	provider := openaicompat.NewProvider("test-key", srv.URL)

	ctx, cancel := context.WithCancel(t.Context())
	out, err := provider.Stream(ctx, llm.Request{
		Model: "stub-model", Messages: []llm.Message{{Role: llm.RoleUser, Text: "hi"}},
	})
	if err != nil {
		t.Fatalf("Stream: %v", err)
	}

	cancel()

	for {
		select {
		case ev, ok := <-out:
			if !ok {
				return // closed after cancel: pass
			}
			if ev.Type == llm.EventError && strings.Contains(ev.Err.Error(), "context canceled") {
				return
			}
		case <-time.After(5 * time.Second):
			t.Fatal("stream did not close after cancellation")
		}
	}
}
