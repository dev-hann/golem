package session_test

import (
	"testing"

	"github.com/dev-hann/golem/internal/session"
)

// ADR-0022: a session_compacted record is a barrier — folding drops
// every conversation event before it and replaces the context with one
// synthesized user message carrying the summary. Title and the turn
// count survive; events after the barrier fold normally.

// compactedRecord is the wire shape of a persisted compaction barrier.
func compactedRecord(summary string) map[string]any {
	return map[string]any{"type": "session_compacted", "session": "x", "summary": summary}
}

// reopenStore returns a fresh store over the same directory so folding
// reloads from disk (Create's cache entry would shadow the appended
// events).
func reopenStore(t *testing.T, store *session.Store) *session.Store {
	t.Helper()

	reopened, err := session.NewStore(store.Dir(), 2)
	if err != nil {
		t.Fatalf("reopening: %v", err)
	}

	return reopened
}

func TestFold_CompactBarrierReplacesPriorHistory(t *testing.T) {
	t.Parallel()

	store := newStore(t, 4)
	sess := mustCreate(t, store)

	appendEvents(t, store, sess.ID, textTurn("first question", "first answer ")...)
	appendEvents(t, store, sess.ID, textTurn("second question", "second answer")...)
	appendEvents(t, store, sess.ID, compactedRecord("SUMMARY-OF-EVERYTHING"))
	appendEvents(t, store, sess.ID, textTurn("after compact", "fresh answer")...)

	got, err := reopenStore(t, store).Get(sess.ID)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}

	if len(got.Messages) != 3 {
		t.Fatalf("messages = %d, want 3 (summary + post-compact user + assistant):\n%+v",
			len(got.Messages), got.Messages)
	}

	if got.Messages[0].Text != "[Context summary]\nSUMMARY-OF-EVERYTHING" {
		t.Errorf("first message = %q, want the context summary", got.Messages[0].Text)
	}
	if got.Messages[0].Role != "user" {
		t.Errorf("summary role = %q, want user", got.Messages[0].Role)
	}
	if got.Messages[2].Text != "fresh answer" || got.Messages[2].Role != "assistant" {
		t.Errorf("third message = %+v, want the post-barrier assistant reply", got.Messages[2])
	}

	for _, m := range got.Messages {
		if m.Text == "first question" || m.Text == "first answer" ||
			m.Text == "second question" || m.Text == "second answer" {
			t.Errorf("pre-barrier content leaked into folded history: %q", m.Text)
		}
	}
}

func TestFold_CompactBarrierKeepsTitleAndTurns(t *testing.T) {
	t.Parallel()

	store := newStore(t, 4)
	sess := mustCreate(t, store)

	appendEvents(t, store, sess.ID, textTurn("q1", "a1")...)
	appendEvents(t, store, sess.ID,
		map[string]any{"type": "session_title", "session": "x", "title": "Kept title"})
	appendEvents(t, store, sess.ID, compactedRecord("SUMMARY"))

	got, err := reopenStore(t, store).Get(sess.ID)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}

	if got.Title != "Kept title" {
		t.Errorf("title = %q, want it preserved across the barrier", got.Title)
	}
	if got.Turns != 1 {
		t.Errorf("turns = %d, want the pre-barrier count preserved", got.Turns)
	}
}

func TestFold_CompactBarrierResetsFirstUserText(t *testing.T) {
	t.Parallel()

	store := newStore(t, 4)
	sess := mustCreate(t, store)

	appendEvents(t, store, sess.ID, textTurn("original opener", "a1")...)
	appendEvents(t, store, sess.ID, compactedRecord("SUMMARY"))

	got, err := reopenStore(t, store).Get(sess.ID)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}

	if got.FirstUserText != "" {
		t.Errorf("FirstUserText = %q, want reset (title side calls must not "+
			"re-summarize a compacted opener)", got.FirstUserText)
	}
}

func TestFold_BarrierWithoutPriorHistoryFoldsSummary(t *testing.T) {
	t.Parallel()

	store := newStore(t, 4)
	sess := mustCreate(t, store)

	appendEvents(t, store, sess.ID, compactedRecord("ONLY A SUMMARY"))

	got, err := reopenStore(t, store).Get(sess.ID)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}

	if len(got.Messages) != 1 || got.Messages[0].Text != "[Context summary]\nONLY A SUMMARY" {
		t.Fatalf("messages = %+v, want the bare summary", got.Messages)
	}
}

// mustCreate starts a session or fails the test.
func mustCreate(t *testing.T, store *session.Store) *session.Session {
	t.Helper()

	sess, err := store.Create("/proj")
	if err != nil {
		t.Fatalf("Create: %v", err)
	}

	return sess
}
