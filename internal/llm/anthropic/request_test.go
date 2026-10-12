package anthropic_test

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/dev-hann/golem/internal/llm"
	anthropicgo "github.com/dev-hann/golem/internal/llm/anthropic"
)

func TestAnthropicStream_RequestShapeConverted(t *testing.T) {
	bodyCh := make(chan []byte, 1)
	srv := startStub(t, func(w http.ResponseWriter, r *http.Request) {
		data, err := io.ReadAll(r.Body)
		if err != nil {
			t.Errorf("read body: %v", err)
		}
		bodyCh <- data
		s := newSSE(t, w)
		s.write("message_start", map[string]any{"type": "message_start", "message": map[string]any{
			"id": "msg_1", "type": "message", "role": "assistant", "content": []any{},
			"usage": usagePayload(),
		}})
		s.write("message_stop", map[string]any{"type": "message_stop"})
	})
	provider := anthropicgo.NewProvider("k", srv.URL)
	req := llm.Request{
		Model:        "stub-model",
		SystemPrompt: "be brief",
		Tools: []llm.ToolSpec{{
			Name:        "read",
			Description: "Read a file.",
			InputSchema: map[string]any{
				"properties": map[string]any{"path": map[string]any{"type": "string"}},
			},
		}},
		Messages: []llm.Message{
			{Role: llm.RoleUser, Text: "read it"},
			{
				Role: llm.RoleAssistant,
				ToolCalls: []llm.ToolCall{
					{ID: "tc_1", Name: "read", Args: json.RawMessage(`{"path":"f"}`)},
				},
			},
			{
				Role: llm.RoleUser,
				ToolResults: []llm.ToolResult{
					{CallID: "tc_1", Name: "read", OK: false, Output: "error: boom"},
				},
			},
		},
	}
	stream, err := provider.Stream(context.Background(), req)
	if err != nil {
		t.Fatalf("Stream: %v", err)
	}
	for ev := range stream {
		_ = ev // drain until close
	}
	var body struct {
		System []struct {
			Text string `json:"text"`
		} `json:"system"`
		Tools []struct {
			Name        string `json:"name"`
			Description string `json:"description"`
		} `json:"tools"`
		Messages []struct {
			Role    string `json:"role"`
			Content []struct {
				Type      string          `json:"type"`
				ID        string          `json:"id"`
				Name      string          `json:"name"`
				Input     json.RawMessage `json:"input"`
				Text      string          `json:"text"`
				Content   json.RawMessage `json:"content"`
				IsError   bool            `json:"is_error"`
				ToolUseID string          `json:"tool_use_id"`
			} `json:"content"`
		} `json:"messages"`
	}
	raw := <-bodyCh
	if err := json.Unmarshal(raw, &body); err != nil {
		t.Fatalf("unmarshal request body: %v\n%s", err, raw)
	}
	if len(body.System) != 1 || body.System[0].Text != "be brief" {
		t.Errorf("system = %+v", body.System)
	}
	if len(body.Tools) != 1 || body.Tools[0].Name != "read" || body.Tools[0].Description != "Read a file." {
		t.Errorf("tools = %+v", body.Tools)
	}
	if len(body.Messages) != 3 {
		t.Fatalf("messages = %d, want 3", len(body.Messages))
	}
	if body.Messages[0].Content[0].Text != "read it" {
		t.Errorf("user text = %+v", body.Messages[0].Content)
	}
	asst := body.Messages[1].Content[0]
	if asst.Type != "tool_use" || asst.ID != "tc_1" || !strings.Contains(string(asst.Input), "path") {
		t.Errorf("assistant tool_use block = %+v", asst)
	}
	res := body.Messages[2].Content[0]
	if res.Type != "tool_result" || res.ToolUseID != "tc_1" || !res.IsError ||
		!strings.Contains(string(res.Content), "error: boom") {
		t.Errorf("tool_result block = %+v", res)
	}
}

func TestAnthropicStream_EmptyModelErrors(t *testing.T) {
	provider := anthropicgo.NewProvider("k", "")
	_, err := provider.Stream(context.Background(), llm.Request{Model: ""})
	if err == nil || !strings.Contains(err.Error(), "model is required") {
		t.Errorf("expected model-required error, got: %v", err)
	}
}
