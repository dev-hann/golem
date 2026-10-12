package session_test

import (
	"encoding/json"
	"path/filepath"
	"testing"

	"github.com/dev-hann/golem/internal/session"
)

func TestNewStore_RejectsZeroCache(t *testing.T) {
	t.Parallel()
	if _, err := session.NewStore(t.TempDir(), 0); err == nil {
		t.Error("cache size 0 should be rejected")
	}
}

func TestGet_MissingSessionErrors(t *testing.T) {
	t.Parallel()
	store := newStore(t, 2)
	if _, err := store.Get("01JZZZZZZZZZZZZZZZZZZZZZZZ"); err == nil {
		t.Error("missing session file should error")
	}
}

func TestRawEvents_ReturnsStoredLines(t *testing.T) {
	t.Parallel()
	store := newStore(t, 2)
	sess, err := store.Create("/proj")
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	appendEvents(t, store, sess.ID, textTurn("hi", "ok")...)
	raw, err := store.RawEvents(sess.ID)
	if err != nil {
		t.Fatalf("RawEvents: %v", err)
	}
	if len(raw) == 0 {
		t.Fatal("no raw events returned")
	}
	types := make([]string, 0, len(raw))
	for _, line := range raw {
		var env struct {
			Type string `json:"type"`
		}
		if err := json.Unmarshal(line, &env); err != nil {
			t.Fatalf("invalid line: %v", err)
		}
		types = append(types, env.Type)
	}
	if types[0] != "session_meta" || types[1] != "user_message" {
		t.Errorf("types = %v", types)
	}
}

func TestRawEvents_InvalidIDErrors(t *testing.T) {
	t.Parallel()
	store := newStore(t, 2)
	if _, err := store.RawEvents("../evil"); err == nil {
		t.Error("invalid id should error")
	}
}

func TestInvalidate_ForcesReloadFromDisk(t *testing.T) {
	t.Parallel()
	store := newStore(t, 4)
	sess, err := store.Create("/proj")
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	// A second store writing to the same dir simulates another process.
	other, err := session.NewStore(store.Dir(), 4)
	if err != nil {
		t.Fatalf("second store: %v", err)
	}
	if err := other.Append(sess.ID, map[string]any{"type": "text_delta", "delta": "external"}); err != nil {
		t.Fatalf("external append: %v", err)
	}
	store.Invalidate(sess.ID)
	reloaded, err := store.Get(sess.ID)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	var sawExternal bool
	for _, msg := range reloaded.Messages {
		_ = msg
	}
	// The external delta belongs to no completed turn assistant message set;
	// folding flushes it as an assistant message at EOF.
	if len(reloaded.Messages) > 0 && reloaded.Messages[len(reloaded.Messages)-1].Text == "external" {
		sawExternal = true
	}
	if !sawExternal {
		t.Errorf("reload after Invalidate missed external write: %+v", reloaded.Messages)
	}
}

func TestDir_MatchesCreationPath(t *testing.T) {
	t.Parallel()
	dir := filepath.Join(t.TempDir(), "nested", "sessions")
	store, err := session.NewStore(dir, 2)
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	if store.Dir() != dir {
		t.Errorf("Dir() = %q, want %q", store.Dir(), dir)
	}
}

func TestApplyMessage_UncachedSessionIsNoop(t *testing.T) {
	t.Parallel()
	store := newStore(t, 2)
	if err := store.ApplyMessage("01JZZZZZZZZZZZZZZZZZZZZZZZ"); err != nil {
		t.Errorf("ApplyMessage on uncached session should be a no-op, got %v", err)
	}
	if err := store.FinishTurn("01JZZZZZZZZZZZZZZZZZZZZZZZ"); err != nil {
		t.Errorf("FinishTurn on uncached session should be a no-op, got %v", err)
	}
}

func TestFinishTurn_IncrementsCount(t *testing.T) {
	t.Parallel()
	store := newStore(t, 4)
	sess, err := store.Create("/proj")
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if err := store.FinishTurn(sess.ID); err != nil {
		t.Fatalf("FinishTurn: %v", err)
	}
	got, err := store.Get(sess.ID)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got.Turns != 1 {
		t.Errorf("turns = %d, want 1", got.Turns)
	}
}
