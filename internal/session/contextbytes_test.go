package session_test

import (
	"testing"
)

// ADR-0025: ContextBytes measures the folded history the way the model
// will replay it — message text, tool-call arguments, tool results.

func TestContextBytes_SumsTextArgsAndResults(t *testing.T) {
	t.Parallel()

	store := newStore(t, 4)
	sess := mustCreate(t, store)

	appendEvents(t, store, sess.ID,
		map[string]any{"type": "user_message", "session": "x", "text": "12345"},
		map[string]any{"type": "text_delta", "session": "x", "turn": "t", "delta": "6789"},
		map[string]any{
			"type": "turn_end", "session": "x", "turn": "t",
			"usage": map[string]any{"input_tokens": 1, "output_tokens": 1},
		},
		map[string]any{
			"type": "tool_call_start", "session": "x", "turn": "t2", "call_id": "c1",
			"tool": "read", "args": map[string]any{"path": "a.go"},
		},
		map[string]any{
			"type": "tool_call_end", "session": "x", "turn": "t2", "call_id": "c1",
			"ok": true, "result": "file contents",
		},
	)

	got, err := reopenStore(t, store).Get(sess.ID)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}

	// 5 (user text) + 4 (assistant text) + len(`{"path":"a.go"}`) + 13 (result).
	want := len("12345") + len("6789") + len(`{"path":"a.go"}`) + len("file contents")

	if got.ContextBytes() != want {
		t.Errorf("ContextBytes() = %d, want %d", got.ContextBytes(), want)
	}
}

func TestContextBytes_EmptySessionIsZero(t *testing.T) {
	t.Parallel()

	store := newStore(t, 4)
	sess := mustCreate(t, store)

	got, err := reopenStore(t, store).Get(sess.ID)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}

	if got.ContextBytes() != 0 {
		t.Errorf("ContextBytes() = %d, want 0 for an empty session", got.ContextBytes())
	}
}
