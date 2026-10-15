package session_test

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/dev-hann/golem/internal/llm"
	"github.com/dev-hann/golem/internal/session"
	"github.com/dev-hann/golem/pkg/golemclient"
)

// newStore opens a store in a temp dir for tests.
func newStore(t *testing.T, cacheSize int) *session.Store {
	t.Helper()
	store, err := session.NewStore(t.TempDir(), cacheSize)
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	return store
}

// appendEvents writes raw wire events to a session file.
func appendEvents(t *testing.T, store *session.Store, id string, events ...any) {
	t.Helper()
	for _, ev := range events {
		if err := store.Append(id, ev); err != nil {
			t.Fatalf("Append: %v", err)
		}
	}
}

// textTurn is a minimal user→assistant turn.
func textTurn(text, reply string) []any {
	return []any{
		map[string]any{"type": "user_message", "session": "x", "text": text},
		map[string]any{"type": "turn_start", "session": "x", "turn": "t"},
		map[string]any{"type": "text_delta", "session": "x", "turn": "t", "delta": reply},
		map[string]any{
			"type": "turn_end", "session": "x", "turn": "t",
			"usage": map[string]any{"input_tokens": 1, "output_tokens": 1},
		},
	}
}

func TestAppend_WritesOneJSONPerLine(t *testing.T) {
	t.Parallel()
	store := newStore(t, 4)
	sess, err := store.Create("/proj")
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	appendEvents(t, store, sess.ID, textTurn("hi", "hello")...)
	summaries, listErr := store.List()
	if listErr != nil {
		t.Fatalf("List: %v", listErr)
	}
	if len(summaries) != 1 || summaries[0].ID != sess.ID {
		t.Fatalf("summaries = %+v", summaries)
	}
	if summaries[0].Turns != 1 {
		t.Errorf("turns = %d, want 1", summaries[0].Turns)
	}
	if summaries[0].Cwd != "/proj" {
		t.Errorf("cwd = %q, want /proj (from the session_meta header line)", summaries[0].Cwd)
	}
}

func TestReplay_FoldsEventsIntoConversation(t *testing.T) {
	t.Parallel()
	store := newStore(t, 4)
	sess, err := store.Create("/proj")
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	appendEvents(t, store, sess.ID, textTurn("hi", "hello ")...)
	appendEvents(t, store, sess.ID, textTurn("again", "there")...)

	// Reload from disk by using a fresh store over the same directory.
	dir := storeDir(t, store)
	reopened, err := session.NewStore(dir, 2)
	if err != nil {
		t.Fatalf("reopening: %v", err)
	}
	loaded, err := reopened.Get(sess.ID)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if len(loaded.Messages) != 4 {
		t.Fatalf("messages = %d, want 4", len(loaded.Messages))
	}
	if loaded.Messages[0].Text != "hi" || loaded.Messages[0].Role != llm.RoleUser {
		t.Errorf("message 0 = %+v", loaded.Messages[0])
	}
	if loaded.Messages[1].Text != "hello " || loaded.Messages[1].Role != llm.RoleAssistant {
		t.Errorf("message 1 = %+v", loaded.Messages[1])
	}
	if loaded.Turns != 2 {
		t.Errorf("turns = %d, want 2", loaded.Turns)
	}
}

func TestReplay_FoldsToolCallsAndResults(t *testing.T) {
	t.Parallel()
	store := newStore(t, 4)
	sess, err := store.Create("/proj")
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	appendEvents(t, store, sess.ID,
		map[string]any{"type": "user_message", "text": "read f"},
		map[string]any{"type": "text_delta", "delta": "reading"},
		map[string]any{
			"type": "tool_call_start", "call_id": "c1", "tool": "read",
			"args": map[string]string{"path": "f"},
		},
		map[string]any{"type": "tool_call_end", "call_id": "c1", "ok": true, "result": "contents"},
		map[string]any{"type": "turn_end", "usage": map[string]int{"input_tokens": 2, "output_tokens": 1}},
	)
	dir := storeDir(t, store)
	reopened, err := session.NewStore(dir, 2)
	if err != nil {
		t.Fatalf("reopening: %v", err)
	}
	loaded, err := reopened.Get(sess.ID)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if len(loaded.Messages) != 3 {
		t.Fatalf("messages = %d, want 3", len(loaded.Messages))
	}
	asst := loaded.Messages[1]
	if len(asst.ToolCalls) != 1 || asst.ToolCalls[0].Name != "read" || asst.Text != "reading" {
		t.Errorf("assistant message = %+v", asst)
	}
	res := loaded.Messages[2].ToolResults
	if len(res) != 1 || res[0].CallID != "c1" || res[0].Output != "contents" {
		t.Errorf("results = %+v", res)
	}
}

func TestReplay_UnknownEventSkipped(t *testing.T) {
	t.Parallel()
	store := newStore(t, 4)
	sess, err := store.Create("/proj")
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	appendEvents(t, store, sess.ID,
		map[string]any{"type": "future_thing", "payload": 1},
		map[string]any{"type": "approval_request", "preview": "rm"},
		map[string]any{"type": "approval_response", "decision": "allow"},
	)
	appendEvents(t, store, sess.ID, textTurn("hi", "ok")...)
	dir := storeDir(t, store)
	reopened, err := session.NewStore(dir, 2)
	if err != nil {
		t.Fatalf("reopening: %v", err)
	}
	loaded, err := reopened.Get(sess.ID)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if len(loaded.Messages) != 2 {
		t.Errorf("unknown events should not add messages, got %d", len(loaded.Messages))
	}
}

func TestReplay_TruncatedTurnStillContributesMessages(t *testing.T) {
	t.Parallel()
	store := newStore(t, 4)
	sess, err := store.Create("/proj")
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	// Crash mid-turn: user message and one delta, no turn_end.
	appendEvents(t, store, sess.ID,
		map[string]any{"type": "user_message", "text": "hi"},
		map[string]any{"type": "text_delta", "delta": "partial"},
	)
	dir := storeDir(t, store)
	reopened, err := session.NewStore(dir, 2)
	if err != nil {
		t.Fatalf("reopening: %v", err)
	}
	loaded, err := reopened.Get(sess.ID)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if len(loaded.Messages) != 2 {
		t.Fatalf("messages = %d, want 2 (user + partial assistant)", len(loaded.Messages))
	}
	if loaded.Turns != 0 {
		t.Errorf("turns = %d, want 0 (no turn_end recorded)", loaded.Turns)
	}
}

func TestCache_EvictsLeastRecentlyActive(t *testing.T) {
	t.Parallel()
	store := newStore(t, 2)
	ids := make([]string, 0, 3)
	for range 3 {
		sess, err := store.Create("/proj")
		if err != nil {
			t.Fatalf("Create: %v", err)
		}
		appendEvents(t, store, sess.ID, textTurn("hi", "ok")...)
		ids = append(ids, sess.ID)
	}
	// s0 should be evicted (oldest); Get reloads it from disk correctly.
	evicted, err := store.Get(ids[0])
	if err != nil {
		t.Fatalf("Get evicted: %v", err)
	}
	if len(evicted.Messages) != 2 {
		t.Errorf("reloaded messages = %d, want 2", len(evicted.Messages))
	}
}

func TestList_OrdersByLastActive(t *testing.T) {
	t.Parallel()
	store := newStore(t, 8)
	ids := make([]string, 0, 3)
	for range 3 {
		sess, err := store.Create("/proj")
		if err != nil {
			t.Fatalf("Create: %v", err)
		}
		ids = append(ids, sess.ID)
		time.Sleep(2 * time.Millisecond)
	}
	// Touch the oldest so it becomes most recent.
	appendEvents(t, store, ids[0], textTurn("hi", "ok")...)
	summaries, err := store.List()
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(summaries) != 3 {
		t.Fatalf("summaries = %d", len(summaries))
	}
	if summaries[0].ID != ids[0] {
		t.Errorf("most recent = %s, want %s", summaries[0].ID, ids[0])
	}
}

func TestGet_RejectsInvalidIDs(t *testing.T) {
	t.Parallel()
	store := newStore(t, 2)
	for _, id := range []string{"", "../../etc/passwd", "short"} {
		if _, err := store.Get(id); err == nil {
			t.Errorf("Get(%q) should fail", id)
		}
		if err := store.Append(id, map[string]string{"type": "x"}); err == nil {
			t.Errorf("Append(%q) should fail", id)
		}
	}
}

func TestAppend_LineFormatIsValidJSONPerLine(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	store, err := session.NewStore(dir, 2)
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	sess, err := store.Create("/proj")
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	appendEvents(t, store, sess.ID, textTurn("hi", "ok")...)
	data, err := os.ReadFile(filepath.Join(dir, sess.ID+".jsonl"))
	if err != nil {
		t.Fatalf("read file: %v", err)
	}
	lines := strings.Split(strings.TrimRight(string(data), "\n"), "\n")
	// 1 session_meta header + 4 turn events.
	if len(lines) != 5 {
		t.Fatalf("lines = %d, want 5", len(lines))
	}
	for i, line := range lines {
		if !json.Valid([]byte(line)) {
			t.Errorf("line %d invalid JSON: %q", i, line)
		}
	}
}

// storeDir returns the directory backing the store.
func storeDir(_ *testing.T, store *session.Store) string {
	return store.Dir()
}

func TestStore_TitleRoundTrip(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	store, err := session.NewStore(dir, 2)
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}

	sess, err := store.Create("/proj")
	if err != nil {
		t.Fatalf("Create: %v", err)
	}

	appendEvents(t, store, sess.ID, textTurn("fix the auth middleware", "done")...)
	appendEvents(t, store, sess.ID, golemclient.SessionTitle{
		Type: golemclient.TypeSessionTitle, Session: sess.ID, Title: "Auth middleware fix",
	})

	// The cached copy predates the append (the server invalidates after a
	// turn); a reopened store proves the record folds back in.
	reopened, err := session.NewStore(dir, 2)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}

	got, err := reopened.Get(sess.ID)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}

	if got.Title != "Auth middleware fix" {
		t.Errorf("Title = %q, want the folded session_title record", got.Title)
	}

	summaries, err := reopened.List()
	if err != nil {
		t.Fatalf("List: %v", err)
	}

	if len(summaries) != 1 || summaries[0].Title != "Auth middleware fix" {
		t.Errorf("summaries = %+v, want the title surfaced", summaries)
	}
}

func TestStore_TitleLastRecordWins(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	store, err := session.NewStore(dir, 2)
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}

	sess, err := store.Create("/proj")
	if err != nil {
		t.Fatalf("Create: %v", err)
	}

	appendEvents(t, store, sess.ID,
		golemclient.SessionTitle{
			Type: golemclient.TypeSessionTitle, Session: sess.ID, Title: "First title",
		},
		golemclient.SessionTitle{
			Type: golemclient.TypeSessionTitle, Session: sess.ID, Title: "Renamed title",
		},
	)

	summaries, err := store.List()
	if err != nil {
		t.Fatalf("List: %v", err)
	}

	if len(summaries) != 1 || summaries[0].Title != "Renamed title" {
		t.Errorf("summaries = %+v, want the latest title record to win", summaries)
	}
}

func TestStore_DeleteRemovesFileAndListing(t *testing.T) {
	t.Parallel()
	store := newStore(t, 4)
	keep, err := store.Create("/keep")
	if err != nil {
		t.Fatalf("Create keep: %v", err)
	}
	gone, err := store.Create("/gone")
	if err != nil {
		t.Fatalf("Create gone: %v", err)
	}
	appendEvents(t, store, gone.ID, textTurn("hi", "hello")...)

	if err := store.Delete(gone.ID); err != nil {
		t.Fatalf("Delete: %v", err)
	}

	if _, err := os.Stat(filepath.Join(store.Dir(), gone.ID+".jsonl")); !os.IsNotExist(err) {
		t.Errorf("deleted session file still on disk (stat err = %v)", err)
	}

	summaries, err := store.List()
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(summaries) != 1 || summaries[0].ID != keep.ID {
		t.Errorf("summaries = %+v, want only %s", summaries, keep.ID)
	}

	// A Get after Delete reports the session gone, not a fold of a
	// missing file.
	if _, err := store.Get(gone.ID); err == nil {
		t.Error("Get after Delete should error")
	}
}

func TestStore_DeleteUnknownIDFails(t *testing.T) {
	t.Parallel()
	store := newStore(t, 2)

	if err := store.Delete("01ARZ3NDEKTSV4RRFFQ69G5FAV"); err == nil {
		t.Error("Delete of a never-created session should error")
	}

	if err := store.Delete("../escape"); err == nil {
		t.Error("Delete of an invalid id should error")
	}
}

// TestStore_AppendDuringDeleteNeverResurrects pins the resurrection race:
// an Append racing a Delete must either win the file (pre-delete append,
// Delete then removes it) or fail with ErrDeleted — it can never re-create
// the file after Delete returned. The window without the fix: Append's
// flag check passes, it drops the lock, Delete marks+removes, Append's
// O_CREATE resurrects.
func TestStore_AppendDuringDeleteNeverResurrects(t *testing.T) {
	t.Parallel()
	store := newStore(t, 4)

	sess, err := store.Create("/proj")
	if err != nil {
		t.Fatalf("Create: %v", err)
	}

	iterations := 200
	var wg sync.WaitGroup

	wg.Add(2)

	go func() { // appender
		defer wg.Done()
		for range iterations {
			_ = store.Append(sess.ID, map[string]any{"type": "text_delta"})
		}
	}()

	go func() { // deleter
		defer wg.Done()
		for range iterations {
			_ = store.Delete(sess.ID)
		}
	}()

	wg.Wait()

	path := filepath.Join(store.Dir(), sess.ID+".jsonl")
	if _, err := os.Stat(path); err == nil {
		t.Errorf("session file resurrected after deletes: %s exists", path)
	} else if !os.IsNotExist(err) {
		t.Errorf("stat %s: %v", path, err)
	}

	// Post-race appends must fail: the session is deleted and stays so.
	if err := store.Append(sess.ID, map[string]any{"type": "text_delta"}); err == nil {
		t.Error("Append after Delete must fail, not resurrect the file")
	} else if !errors.Is(err, session.ErrDeleted) {
		t.Errorf("Append after Delete = %v, want ErrDeleted", err)
	}
}
