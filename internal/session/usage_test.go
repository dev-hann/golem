package session_test

import (
	"testing"

	"github.com/dev-hann/golem/internal/session"
)

// usageTurn is a text turn carrying explicit usage numbers.
func usageTurn(text string, in, out int64) []any {
	return []any{
		map[string]any{"type": "user_message", "session": "x", "text": text},
		map[string]any{"type": "turn_start", "session": "x", "turn": "t"},
		map[string]any{"type": "text_delta", "session": "x", "turn": "t", "delta": "ok"},
		map[string]any{
			"type": "turn_end", "session": "x", "turn": "t",
			"usage": map[string]any{"input_tokens": in, "output_tokens": out},
		},
	}
}

func TestFold_AccumulatesUsageAcrossTurns(t *testing.T) {
	t.Parallel()

	store := newStore(t, 2)
	sess, err := store.Create("/proj")
	if err != nil {
		t.Fatalf("Create: %v", err)
	}

	appendEvents(t, store, sess.ID, usageTurn("a", 100, 10)...)
	appendEvents(t, store, sess.ID, usageTurn("b", 50, 5)...)
	store.Invalidate(sess.ID)

	loaded, err := store.Get(sess.ID)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}

	if loaded.TokensIn != 150 || loaded.TokensOut != 15 {
		t.Errorf("usage = %d/%d, want 150/15", loaded.TokensIn, loaded.TokensOut)
	}
}

func TestFold_UsageMissingFieldsZero(t *testing.T) {
	t.Parallel()

	store := newStore(t, 2)
	sess, err := store.Create("/proj")
	if err != nil {
		t.Fatalf("Create: %v", err)
	}

	// turn_end with no usage object at all (legacy or interrupted turn).
	appendEvents(t, store, sess.ID, map[string]any{
		"type": "user_message", "session": "x", "text": "hi",
	}, map[string]any{"type": "turn_end", "session": "x", "turn": "t"})
	store.Invalidate(sess.ID)

	loaded, err := store.Get(sess.ID)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}

	if loaded.TokensIn != 0 || loaded.TokensOut != 0 {
		t.Errorf("usage = %d/%d, want 0/0", loaded.TokensIn, loaded.TokensOut)
	}
}

func TestFold_UsageKeptThroughCacheReload(t *testing.T) {
	t.Parallel()

	store := newStore(t, 2)
	sess, err := store.Create("/proj")
	if err != nil {
		t.Fatalf("Create: %v", err)
	}

	appendEvents(t, store, sess.ID, usageTurn("a", 10, 1)...)
	store.Invalidate(sess.ID)

	loaded, err := store.Get(sess.ID)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}

	if loaded.TokensIn != 10 || loaded.TokensOut != 1 {
		t.Errorf("usage after reload = %d/%d, want 10/1", loaded.TokensIn, loaded.TokensOut)
	}
}

// Compile-time contract: Session exposes the accumulated counters.
var _ = session.Session{}.TokensIn
