package session_test

import (
	"bytes"
	"encoding/json"
	"errors"
	"testing"

	"github.com/dev-hann/golem/internal/llm"
	"github.com/dev-hann/golem/internal/session"
)

// forkFixture is a titled parent with two completed text turns (t1, t2)
// whose raw lines and folded state are known.
func forkFixture(t *testing.T, store *session.Store) *session.Session {
	t.Helper()

	sess, err := store.Create("/proj")
	if err != nil {
		t.Fatalf("Create: %v", err)
	}

	appendEvents(t, store, sess.ID, forkTurn("t1", "first", "one ")...)
	appendEvents(t, store, sess.ID, forkTurn("t2", "second", "two")...)
	appendEvents(t, store, sess.ID,
		map[string]any{"type": "session_title", "session": "x", "title": "Fix auth"},
	)

	return sess
}

// forkTurn is one completed user→assistant text turn.
func forkTurn(turn, user, reply string) []any {
	return []any{
		map[string]any{"type": "user_message", "session": "x", "text": user},
		map[string]any{"type": "turn_start", "session": "x", "turn": turn},
		map[string]any{"type": "text_delta", "session": "x", "turn": turn, "delta": reply},
		map[string]any{
			"type": "turn_end", "session": "x", "turn": turn,
			"usage": map[string]any{"input_tokens": 1, "output_tokens": 1},
		},
	}
}

// forkRawLines returns a session file's parsed JSONL lines.
func forkRawLines(t *testing.T, store *session.Store, id string) []map[string]any {
	t.Helper()

	raw, err := store.RawEvents(id)
	if err != nil {
		t.Fatalf("RawEvents(%s): %v", id, err)
	}

	lines := make([]map[string]any, 0, len(raw))
	for _, r := range raw {
		var m map[string]any
		if err := json.Unmarshal(r, &m); err != nil {
			t.Fatalf("parsing line: %v", err)
		}
		lines = append(lines, m)
	}

	return lines
}

// wantForkLine asserts one copied line: same type, session rewritten to
// the child, every other field equal to the parent's line.
func wantForkLine(t *testing.T, got, parent map[string]any, childID, where string) {
	t.Helper()

	if got["type"] != parent["type"] {
		t.Errorf("%s: type = %v, want %v", where, got["type"], parent["type"])
	}

	if got["session"] != childID {
		t.Errorf("%s: session = %v, want child %s", where, got["session"], childID)
	}

	for k, v := range parent {
		if k == "session" {
			continue
		}
		if !jsonEqual(got[k], v) {
			t.Errorf("%s: field %q = %v, want %v", where, k, got[k], v)
		}
	}
}

// jsonEqual compares two decoded JSON values (maps are not comparable).
func jsonEqual(a, b any) bool {
	ab, err := json.Marshal(a)
	if err != nil {
		return false
	}

	bb, err := json.Marshal(b)
	if err != nil {
		return false
	}

	return bytes.Equal(ab, bb)
}

func TestFork_CopiesRawEventsThroughLastCompleteTurn(t *testing.T) {
	t.Parallel()

	store := newStore(t, 4)
	parent := forkFixture(t, store)

	child, err := store.Fork(parent.ID, "")
	if err != nil {
		t.Fatalf("Fork: %v", err)
	}

	if child.ID == parent.ID || child.ID == "" {
		t.Fatalf("child id = %q, want a fresh id", child.ID)
	}

	lines := forkRawLines(t, store, child.ID)
	parentLines := forkRawLines(t, store, parent.ID)

	// Child = meta + lineage + the parent's event lines through t2's
	// turn_end + the fork title. The parent's trailing session_title
	// record lies after the boundary and is not copied; the title comes
	// back as the child's own fork-title record instead.
	want := 2 + len(parentLines) - 1 // meta + lineage + events + fork title
	if len(lines) != want {
		t.Fatalf("child lines = %d, want %d", len(lines), want)
	}

	if lines[0]["type"] != "session_meta" || lines[0]["cwd"] != parent.Cwd {
		t.Errorf("child meta = %v, want session_meta with cwd %q", lines[0], parent.Cwd)
	}

	if lines[1]["type"] != "session_forked" || lines[1]["parent"] != parent.ID ||
		lines[1]["through_turn"] != "t2" {
		t.Errorf("lineage = %v, want parent %s through t2", lines[1], parent.ID)
	}

	for i := 1; i < len(parentLines)-1; i++ { // skip parent meta + trailing title
		wantForkLine(t, lines[1+i], parentLines[i], child.ID, "copied line")
	}

	if last := lines[len(lines)-1]; last["type"] != "session_title" ||
		last["title"] != "Fix auth (fork #1)" {
		t.Errorf("last line = %v, want title Fix auth (fork #1)", last)
	}
}

func TestFork_ThroughSpecificTurn_TruncatesAtTurnEnd(t *testing.T) {
	t.Parallel()

	store := newStore(t, 4)
	parent := forkFixture(t, store)

	child, err := store.Fork(parent.ID, "t1")
	if err != nil {
		t.Fatalf("Fork: %v", err)
	}

	lines := forkRawLines(t, store, child.ID)

	// meta + lineage + turn t1's 4 lines + fork title: the parent's title
	// record lies after the boundary and is not copied, but the parent
	// is titled (fold), so the child gets the fork title anyway — the
	// title is session-level like opencode's column, not positional.
	if len(lines) != 7 {
		t.Fatalf("child lines = %d, want 7", len(lines))
	}

	if lines[1]["through_turn"] != "t1" {
		t.Errorf("through_turn = %v, want t1", lines[1]["through_turn"])
	}

	if last := lines[len(lines)-1]; last["type"] != "session_title" ||
		last["title"] != "Fix auth (fork #1)" {
		t.Errorf("last line = %v, want fork title", last)
	}
}

func TestFork_ChildFoldsIdenticallyToParentPrefix(t *testing.T) {
	t.Parallel()

	store := newStore(t, 4)
	parent := forkFixture(t, store)

	child, err := store.Fork(parent.ID, "")
	if err != nil {
		t.Fatalf("Fork: %v", err)
	}

	loaded, err := reopenStore(t, store).Get(child.ID)
	if err != nil {
		t.Fatalf("Get(child): %v", err)
	}

	if loaded.Turns != 2 {
		t.Errorf("child turns = %d, want 2", loaded.Turns)
	}

	if loaded.Cwd != parent.Cwd {
		t.Errorf("child cwd = %q, want %q", loaded.Cwd, parent.Cwd)
	}

	wantMsgs := []struct{ role, text string }{
		{string(llm.RoleUser), "first"},
		{string(llm.RoleAssistant), "one "},
		{string(llm.RoleUser), "second"},
		{string(llm.RoleAssistant), "two"},
	}

	if len(loaded.Messages) != len(wantMsgs) {
		t.Fatalf("child messages = %d, want %d", len(loaded.Messages), len(wantMsgs))
	}

	for i, w := range wantMsgs {
		if loaded.Messages[i].Role != llm.Role(w.role) || loaded.Messages[i].Text != w.text {
			t.Errorf("message %d = %+v, want %s/%q", i, loaded.Messages[i], w.role, w.text)
		}
	}
}

func TestFork_InheritsCompactionBarrier(t *testing.T) {
	t.Parallel()

	store := newStore(t, 4)
	parent := forkFixture(t, store)

	appendEvents(t, store, parent.ID, compactedRecord("Sum before fork"))
	appendEvents(t, store, parent.ID, forkTurn("t3", "third", "tres")...)

	child, err := store.Fork(parent.ID, "")
	if err != nil {
		t.Fatalf("Fork: %v", err)
	}

	loaded, err := reopenStore(t, store).Get(child.ID)
	if err != nil {
		t.Fatalf("Get(child): %v", err)
	}

	// ADR-0022 fold: the barrier drops prior context into one summary
	// message; turns keep counting (t1, t2 before the barrier, t3 after).
	if loaded.Turns != 3 {
		t.Errorf("child turns = %d, want 3", loaded.Turns)
	}

	if len(loaded.Messages) != 3 {
		t.Fatalf("child messages = %d, want 3 (summary + third + tres)", len(loaded.Messages))
	}

	if loaded.Messages[0].Text != "[Context summary]\nSum before fork" ||
		loaded.Messages[0].Role != llm.RoleUser {
		t.Errorf("message 0 = %+v, want the barrier summary", loaded.Messages[0])
	}
}

func TestFork_EmptyParentFails(t *testing.T) {
	t.Parallel()

	store := newStore(t, 4)

	sess, err := store.Create("/proj")
	if err != nil {
		t.Fatalf("Create: %v", err)
	}

	if _, err := store.Fork(sess.ID, ""); !errors.Is(err, session.ErrForkEmpty) {
		t.Fatalf("Fork(empty) err = %v, want ErrForkEmpty", err)
	}
}

func TestFork_UnknownTurnFails(t *testing.T) {
	t.Parallel()

	store := newStore(t, 4)
	parent := forkFixture(t, store)

	if _, err := store.Fork(parent.ID, "nonexistent"); !errors.Is(err, session.ErrForkTurn) {
		t.Fatalf("Fork(unknown turn) err = %v, want ErrForkTurn", err)
	}
}

func TestFork_ParentKeptIntact(t *testing.T) {
	t.Parallel()

	store := newStore(t, 4)
	parent := forkFixture(t, store)

	before := forkRawLines(t, store, parent.ID)

	if _, err := store.Fork(parent.ID, ""); err != nil {
		t.Fatalf("Fork: %v", err)
	}

	after := forkRawLines(t, store, parent.ID)

	if len(before) != len(after) {
		t.Fatalf("parent lines = %d, want %d (untouched)", len(after), len(before))
	}

	for i := range before {
		b, _ := json.Marshal(before[i])
		a, _ := json.Marshal(after[i])
		if !bytes.Equal(b, a) {
			t.Errorf("parent line %d changed: %s → %s", i, b, a)
		}
	}
}

func TestFork_TitleIncrementsForkSuffix(t *testing.T) {
	t.Parallel()

	store := newStore(t, 4)
	parent := forkFixture(t, store)

	first, err := store.Fork(parent.ID, "")
	if err != nil {
		t.Fatalf("Fork #1: %v", err)
	}

	second, err := store.Fork(parent.ID, "")
	if err != nil {
		t.Fatalf("Fork #2: %v", err)
	}

	// A fork of a fork strips its own suffix first, so siblings share a
	// base and increment.
	forkOfFork, err := store.Fork(second.ID, "")
	if err != nil {
		t.Fatalf("Fork of fork: %v", err)
	}

	lines := forkRawLines(t, store, first.ID)
	if last := lines[len(lines)-1]; last["title"] != "Fix auth (fork #1)" {
		t.Errorf("first fork title = %v, want Fix auth (fork #1)", last["title"])
	}

	lines = forkRawLines(t, store, second.ID)
	if last := lines[len(lines)-1]; last["title"] != "Fix auth (fork #2)" {
		t.Errorf("second fork title = %v, want Fix auth (fork #2)", last["title"])
	}

	lines = forkRawLines(t, store, forkOfFork.ID)
	if last := lines[len(lines)-1]; last["title"] != "Fix auth (fork #3)" {
		t.Errorf("fork-of-fork title = %v, want Fix auth (fork #3)", last["title"])
	}
}
