package session_test

import (
	"fmt"
	"testing"
	"time"

	"github.com/dev-hann/golem/internal/session"
)

// replayBudget is the Phase 7 acceptance bound: folding a 500-turn
// JSONL takes under 100 ms (ROADMAP "Benchmark: session replay of
// 500-turn JSONL < 100 ms").
const (
	replayTurns  = 500
	replayBudget = 100 * time.Millisecond
)

// buildReplaySession writes a 500-turn session through the public API
// (session_meta header + user/delta/end per turn) into a fresh store
// and returns a second store over the same directory so folding starts
// cold.
func buildReplaySession(t *testing.T) (store *session.Store, id string) {
	t.Helper()

	store, err := session.NewStore(t.TempDir(), 1)
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}

	sess, err := store.Create("/proj")
	if err != nil {
		t.Fatalf("Create: %v", err)
	}

	for i := range replayTurns {
		events := []any{
			map[string]any{"type": "user_message", "session": sess.ID, "text": fmt.Sprintf("question %d", i)},
			map[string]any{"type": "turn_start", "session": sess.ID, "turn": fmt.Sprintf("t%d", i)},
			map[string]any{
				"type": "text_delta", "session": sess.ID, "turn": fmt.Sprintf("t%d", i),
				"delta": fmt.Sprintf("answer %d: the quick brown fox jumps over the lazy dog", i),
			},
			map[string]any{
				"type": "turn_end", "session": sess.ID, "turn": fmt.Sprintf("t%d", i),
				"usage": map[string]any{"input_tokens": 10, "output_tokens": 10},
			},
		}

		for _, ev := range events {
			if err := store.Append(sess.ID, ev); err != nil {
				t.Fatalf("Append: %v", err)
			}
		}
	}

	cold, err := session.NewStore(store.Dir(), 1)
	if err != nil {
		t.Fatalf("cold store: %v", err)
	}

	return cold, sess.ID
}

// TestReplayBenchmark_500Turns is the Phase 7 budget assertion: a cold
// fold of a 500-turn file must land under 100 ms (the roadmap's replay
// acceptance criterion), and the fold must be correct — 1000 messages,
// 500 counted turns.
func TestReplayBenchmark_500Turns(t *testing.T) {
	t.Parallel()

	store, id := buildReplaySession(t)

	start := time.Now()

	sess, err := store.Get(id)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}

	elapsed := time.Since(start)

	if len(sess.Messages) != 2*replayTurns {
		t.Fatalf("messages = %d, want %d", len(sess.Messages), 2*replayTurns)
	}
	if sess.Turns != replayTurns {
		t.Errorf("turns = %d, want %d", sess.Turns, replayTurns)
	}
	if sess.Messages[0].Text != "question 0" || sess.Messages[1].Text[:8] != "answer 0" {
		t.Errorf("first turn folded wrong: %q / %q", sess.Messages[0].Text, sess.Messages[1].Text)
	}
	if elapsed >= replayBudget {
		t.Errorf("cold replay of %d turns took %v, budget %v", replayTurns, elapsed, replayBudget)
	}
}

// BenchmarkReplay_500Turns measures the cold fold for `go bench`.
func BenchmarkReplay_500Turns(b *testing.B) {
	store, err := session.NewStore(b.TempDir(), 1)
	if err != nil {
		b.Fatalf("NewStore: %v", err)
	}

	sess, err := store.Create("/proj")
	if err != nil {
		b.Fatalf("Create: %v", err)
	}

	for i := range replayTurns {
		events := []any{
			map[string]any{"type": "user_message", "session": sess.ID, "text": fmt.Sprintf("question %d", i)},
			map[string]any{"type": "turn_start", "session": sess.ID, "turn": fmt.Sprintf("t%d", i)},
			map[string]any{
				"type": "text_delta", "session": sess.ID, "turn": fmt.Sprintf("t%d", i),
				"delta": fmt.Sprintf("answer %d: the quick brown fox jumps over the lazy dog", i),
			},
			map[string]any{
				"type": "turn_end", "session": sess.ID, "turn": fmt.Sprintf("t%d", i),
				"usage": map[string]any{"input_tokens": 10, "output_tokens": 10},
			},
		}

		for _, ev := range events {
			if err := store.Append(sess.ID, ev); err != nil {
				b.Fatalf("Append: %v", err)
			}
		}
	}

	b.ResetTimer()

	for range b.N {
		cold, err := session.NewStore(store.Dir(), 1)
		if err != nil {
			b.Fatalf("cold store: %v", err)
		}

		if _, err := cold.Get(sess.ID); err != nil {
			b.Fatalf("Get: %v", err)
		}
	}
}
