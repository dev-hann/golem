package anthropic_test

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/dev-hann/golem/internal/llm"
	anthropicgo "github.com/dev-hann/golem/internal/llm/anthropic"
)

// sseWriter emits Anthropic-style SSE events.
type sseWriter struct {
	t      *testing.T
	w      http.ResponseWriter
	flush  http.Flusher
	status int
}

// write sends one named event with a JSON payload.
func (s *sseWriter) write(event string, payload any) {
	s.t.Helper()
	if s.status != http.StatusOK {
		s.w.WriteHeader(s.status)
		s.flush.Flush()
		return
	}
	data, err := json.Marshal(payload)
	if err != nil {
		s.t.Fatalf("marshal sse payload: %v", err)
	}
	fmt.Fprintf(s.w, "event: %s\ndata: %s\n\n", event, data)
	s.flush.Flush()
}

// newSSE builds a streaming writer with a default 200 status.
func newSSE(t *testing.T, w http.ResponseWriter) *sseWriter {
	t.Helper()
	w.Header().Set("Content-Type", "text/event-stream")
	return &sseWriter{t: t, w: w, flush: w.(http.Flusher), status: http.StatusOK}
}

// usagePayload builds a fully-populated usage object (SDK marks all fields
// required).
func usagePayload() map[string]any {
	return map[string]any{
		"input_tokens":                11,
		"output_tokens":               7,
		"cache_creation_input_tokens": 0,
		"cache_read_input_tokens":     0,
		"output_tokens_details":       map[string]any{},
	}
}

// startStub launches a stub Messages API server returning the given handler.
func startStub(t *testing.T, handler http.HandlerFunc) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(handler)
	t.Cleanup(srv.Close)
	return srv
}

// streamRequest opens a stream against provider for a minimal request.
func streamRequest(
	t *testing.T,
	baseURL string,
) (<-chan llm.Event, context.CancelFunc) {
	t.Helper()
	provider := anthropicgo.NewProvider("test-key", baseURL)
	ctx, cancel := context.WithCancel(t.Context())
	stream, err := provider.Stream(ctx, llm.Request{
		Model:    "stub-model",
		Messages: []llm.Message{{Role: llm.RoleUser, Text: "hi"}},
	})
	if err != nil {
		cancel()
		t.Fatalf("Stream: %v", err)
	}
	return stream, cancel
}

// drain reads events until the channel closes or the deadline hits.
func drain(t *testing.T, stream <-chan llm.Event) []llm.Event {
	t.Helper()
	var events []llm.Event
	deadline := time.After(5 * time.Second)
	for {
		select {
		case ev, ok := <-stream:
			if !ok {
				return events
			}
			events = append(events, ev)
		case <-deadline:
			t.Fatal("stream did not close within deadline")
			return nil
		}
	}
}

func TestAnthropicStream_NormalizesTextDeltas(t *testing.T) {
	srv := startStub(t, func(w http.ResponseWriter, _ *http.Request) {
		s := newSSE(t, w)
		s.write("message_start", map[string]any{"type": "message_start", "message": map[string]any{
			"id": "msg_1", "type": "message", "role": "assistant", "content": []any{},
			"usage": usagePayload(),
		}})
		s.write("content_block_start", map[string]any{
			"type": "content_block_start", "index": 0,
			"content_block": map[string]any{"type": "text", "text": ""},
		})
		s.write("content_block_delta", map[string]any{
			"type": "content_block_delta", "index": 0,
			"delta": map[string]any{"type": "text_delta", "text": "Hello "},
		})
		s.write("content_block_delta", map[string]any{
			"type": "content_block_delta", "index": 0,
			"delta": map[string]any{"type": "text_delta", "text": "world"},
		})
		s.write("message_delta", map[string]any{
			"type": "message_delta", "delta": map[string]any{"stop_reason": "end_turn"},
			"usage": usagePayload(),
		})
		s.write("message_stop", map[string]any{"type": "message_stop"})
	})
	stream, cancel := streamRequest(t, srv.URL)
	defer cancel()
	events := drain(t, stream)
	var text string
	for _, ev := range events {
		if ev.Type == llm.EventTextDelta {
			text += ev.Delta
		}
	}
	if text != "Hello world" {
		t.Errorf("text = %q, want %q", text, "Hello world")
	}
	if !hasUsage(events, 11, 7) {
		t.Errorf("usage event missing or wrong: %+v", events)
	}
}

func TestAnthropicStream_NormalizesToolUseBlocks(t *testing.T) {
	srv := startStub(t, func(w http.ResponseWriter, _ *http.Request) {
		s := newSSE(t, w)
		s.write("message_start", map[string]any{"type": "message_start", "message": map[string]any{
			"id": "msg_1", "type": "message", "role": "assistant", "content": []any{},
			"usage": usagePayload(),
		}})
		s.write("content_block_start", map[string]any{
			"type": "content_block_start", "index": 1,
			"content_block": map[string]any{
				"type": "tool_use", "id": "tc_1", "name": "read", "input": map[string]any{},
			},
		})
		s.write("content_block_delta", map[string]any{
			"type": "content_block_delta", "index": 1,
			"delta": map[string]any{"type": "input_json_delta", "partial_json": `{"path":`},
		})
		s.write("content_block_delta", map[string]any{
			"type": "content_block_delta", "index": 1,
			"delta": map[string]any{"type": "input_json_delta", "partial_json": `"f.txt"}`},
		})
		s.write("message_delta", map[string]any{
			"type": "message_delta", "delta": map[string]any{"stop_reason": "tool_use"},
			"usage": usagePayload(),
		})
		s.write("message_stop", map[string]any{"type": "message_stop"})
	})
	stream, cancel := streamRequest(t, srv.URL)
	defer cancel()
	events := drain(t, stream)
	var calls []llm.ToolCall
	for _, ev := range events {
		if ev.Type == llm.EventToolCalls {
			calls = append(calls, ev.ToolCalls...)
		}
	}
	if len(calls) != 1 {
		t.Fatalf("tool calls = %d, want 1 (events: %+v)", len(calls), events)
	}
	call := calls[0]
	if call.ID != "tc_1" || call.Name != "read" {
		t.Errorf("call id/name = %q/%q, want tc_1/read", call.ID, call.Name)
	}
	var args map[string]string
	if err := json.Unmarshal(call.Args, &args); err != nil {
		t.Fatalf("args not valid JSON: %v", err)
	}
	if args["path"] != "f.txt" {
		t.Errorf("args = %v, want path f.txt", args)
	}
}

func TestAnthropicStream_SurfacesAPIError(t *testing.T) {
	srv := startStub(t, func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		w.Header().Set("Content-Type", "application/json")
		if _, err := w.Write([]byte(`{"type":"error","error":{"type":"invalid_request_error","message":"bad input"}}`)); err != nil {
			t.Errorf("write error body: %v", err)
		}
	})
	stream, cancel := streamRequest(t, srv.URL)
	defer cancel()
	events := drain(t, stream)
	found := false
	for _, ev := range events {
		if ev.Type == llm.EventError {
			found = true
			if !strings.Contains(ev.Err.Error(), "anthropic") {
				t.Errorf("error should mention anthropic, got: %v", ev.Err)
			}
		}
	}
	if !found {
		t.Fatalf("expected error event, got %+v", events)
	}
}

func TestAnthropicStream_ContextCancelClosesChannel(t *testing.T) {
	release := make(chan struct{})
	srv := startStub(t, func(w http.ResponseWriter, _ *http.Request) {
		s := newSSE(t, w)
		s.write("message_start", map[string]any{"type": "message_start", "message": map[string]any{
			"id": "msg_1", "type": "message", "role": "assistant", "content": []any{},
			"usage": usagePayload(),
		}})
		s.write("content_block_delta", map[string]any{
			"type": "content_block_delta", "index": 0,
			"delta": map[string]any{"type": "text_delta", "text": "partial"},
		})
		<-release // hang until test teardown or client disconnect
	})
	stream, cancel := streamRequest(t, srv.URL)
	defer cancel()
	go func() {
		time.Sleep(300 * time.Millisecond)
		cancel()
	}()
	closed := make(chan struct{})
	var events []llm.Event
	go func() {
		defer close(closed)
		for ev := range stream {
			events = append(events, ev)
		}
	}()
	select {
	case <-closed:
	case <-time.After(5 * time.Second):
		t.Fatal("channel did not close after cancel")
	}
	sawError := false
	for _, ev := range events {
		if ev.Type == llm.EventError {
			sawError = true
		}
	}
	if !sawError {
		t.Errorf("expected error event on cancel, got %+v", events)
	}
	close(release)
}

// hasUsage reports whether events carry the expected usage numbers.
func hasUsage(events []llm.Event, in, out int64) bool {
	for _, ev := range events {
		if ev.Type == llm.EventUsage && ev.InputTokens == in && ev.OutputTokens == out {
			return true
		}
	}
	return false
}
