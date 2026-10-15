package session_test

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/dev-hann/golem/internal/session"
)

// ADR-0029: a cached session keeps its live folder and folded frontier
// (byte offset). A Get after appends folds only the new complete lines —
// no Invalidate round-trip; the server dropped its post-turn
// invalidations on this guarantee.

// sessionPath is the JSONL file backing a session id.
func sessionPath(store *session.Store, id string) string {
	return filepath.Join(store.Dir(), id+".jsonl")
}

// appendRaw writes bytes to the session file verbatim — the shape a
// crash leaves behind (Store.Append always terminates lines).
func appendRaw(t *testing.T, store *session.Store, id, text string) {
	t.Helper()

	path := sessionPath(store, id)
	f, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0o644) //nolint:gosec // test file
	if err != nil {
		t.Fatalf("open %s: %v", path, err)
	}
	defer f.Close() //nolint:errcheck // append-only handle

	if _, err := f.WriteString(text); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}

func TestGet_FoldsAppendedTurnsOnCacheHit(t *testing.T) {
	t.Parallel()

	store := newStore(t, 4)
	sess := mustCreate(t, store)

	appendEvents(t, store, sess.ID, textTurn("hi", "hello")...)

	first, err := store.Get(sess.ID) // cache hit: resumes from the frontier
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if len(first.Messages) != 2 || first.Turns != 1 {
		t.Fatalf("first Get: messages = %d, turns = %d, want 2/1",
			len(first.Messages), first.Turns)
	}

	appendEvents(t, store, sess.ID, textTurn("again", "there")...)

	second, err := store.Get(sess.ID) // must resume, not serve the stale copy
	if err != nil {
		t.Fatalf("Get after append: %v", err)
	}
	if len(second.Messages) != 4 || second.Turns != 2 {
		t.Fatalf("second Get: messages = %d, turns = %d, want 4/2",
			len(second.Messages), second.Turns)
	}
	if second.Messages[2].Text != "again" || second.Messages[3].Text != "there" {
		t.Errorf("resumed messages = [%s, %s], want the appended turn", second.Messages[2].Text, second.Messages[3].Text)
	}
}

func TestGet_UnterminatedTailFoldsIntoSnapshotOnly(t *testing.T) {
	t.Parallel()

	store := newStore(t, 4)
	sess := mustCreate(t, store)

	appendEvents(t, store, sess.ID, textTurn("hi", "hello")...)

	if _, err := store.Get(sess.ID); err != nil {
		t.Fatalf("Get: %v", err)
	}

	// Crash-truncated line: no trailing newline yet.
	appendRaw(t, store, sess.ID, `{"type":"text_delta","delta":"par"}`)

	partial, err := store.Get(sess.ID)
	if err != nil {
		t.Fatalf("Get with tail: %v", err)
	}
	if len(partial.Messages) != 3 || partial.Messages[2].Text != "par" {
		t.Fatalf("partial messages = %+v, want the trailing 'par' assistant message", partial.Messages)
	}
	if partial.Turns != 1 {
		t.Errorf("partial turns = %d, want 1 (tail has no turn_end)", partial.Turns)
	}

	// Idempotence: reading again before the line completes changes nothing.
	stable, err := store.Get(sess.ID)
	if err != nil {
		t.Fatalf("Get again: %v", err)
	}
	if len(stable.Messages) != 3 || stable.Messages[2].Text != "par" {
		t.Fatalf("repeat Get = %+v, want the same snapshot", stable.Messages)
	}

	// The line completes and closes the turn: the tail refolds as part of
	// the completed line — exactly once, never doubled.
	appendRaw(t, store, sess.ID,
		"\n"+`{"type":"turn_end","usage":{"input_tokens":1,"output_tokens":1}}`+"\n")

	done, err := store.Get(sess.ID)
	if err != nil {
		t.Fatalf("Get after completion: %v", err)
	}
	if done.Turns != 2 {
		t.Errorf("turns = %d, want 2", done.Turns)
	}
	if len(done.Messages) != 3 || done.Messages[2].Text != "par" {
		t.Fatalf("completed messages = %+v, want 'par' folded exactly once", done.Messages)
	}
}

func TestGet_FileShrunkBehindTheCacheReloads(t *testing.T) {
	t.Parallel()

	store := newStore(t, 4)
	sess := mustCreate(t, store)

	appendEvents(t, store, sess.ID, textTurn("hi", "hello")...)

	if _, err := store.Get(sess.ID); err != nil {
		t.Fatalf("Get: %v", err)
	}

	// An external rewrite (editor, compaction tool) shrinks the file back
	// to its header line.
	header := fmt.Sprintf(`{"type":"session_meta","session":%q,"cwd":"/proj"}`+"\n", sess.ID)
	if err := os.WriteFile(sessionPath(store, sess.ID), []byte(header), 0o644); err != nil {
		t.Fatalf("rewrite: %v", err)
	}

	got, err := store.Get(sess.ID)
	if err != nil {
		t.Fatalf("Get after shrink: %v", err)
	}
	if len(got.Messages) != 0 {
		t.Fatalf("messages = %+v, want empty (file holds only the header)", got.Messages)
	}
	if got.Turns != 0 {
		t.Errorf("turns = %d, want 0", got.Turns)
	}
}

// C6: List caches summaries keyed by (size, mtime) — an unchanged file
// never refolds, a changed one does.

func TestList_ReflectsAppendsWithoutInvalidation(t *testing.T) {
	t.Parallel()

	store := newStore(t, 4)
	sess := mustCreate(t, store)

	before, err := store.List() // caches the summary
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(before) != 1 || before[0].Turns != 0 {
		t.Fatalf("before = %+v, want one empty session", before)
	}

	appendEvents(t, store, sess.ID, textTurn("hi", "hello")...)

	after, err := store.List()
	if err != nil {
		t.Fatalf("List after append: %v", err)
	}
	if len(after) != 1 || after[0].Turns != 1 {
		t.Fatalf("after = %+v, want turns = 1 (summary cache must notice the file changed)", after)
	}
}

func TestList_ReflectsExternalShrink(t *testing.T) {
	t.Parallel()

	store := newStore(t, 4)
	sess := mustCreate(t, store)

	appendEvents(t, store, sess.ID, textTurn("hi", "hello")...)

	before, err := store.List() // caches the summary
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if before[0].Turns != 1 {
		t.Fatalf("before = %+v, want turns = 1", before)
	}

	header := fmt.Sprintf(`{"type":"session_meta","session":%q,"cwd":"/proj"}`+"\n", sess.ID)
	if err := os.WriteFile(sessionPath(store, sess.ID), []byte(header), 0o644); err != nil {
		t.Fatalf("rewrite: %v", err)
	}

	after, err := store.List()
	if err != nil {
		t.Fatalf("List after shrink: %v", err)
	}
	if len(after) != 1 || after[0].Turns != 0 {
		t.Fatalf("after = %+v, want the shrunk summary (turns = 0)", after)
	}
}
