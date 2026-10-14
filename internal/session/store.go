// Package session persists conversations as append-only JSONL files and
// folds them back into conversation state (see docs/DECISIONS.md ADR-0008).
// The stored lines are the same wire events defined in docs/PROTOCOL.md.
package session

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/oklog/ulid/v2"

	"github.com/dev-hann/golem/internal/llm"
)

// fileSuffix is the extension for session files.
const fileSuffix = ".jsonl"

// Session is a conversation: identity plus folded message history.
// Session is the folded state of one stored session.
type Session struct {
	ID       string
	Cwd      string
	Title    string
	Created  time.Time
	Turns    int
	Messages []llm.Message
}

// Store is an append-only session store with a bounded LRU cache.
type Store struct {
	dir       string
	maxCached int

	mu    sync.Mutex
	cache map[string]*cacheEntry
}

// cacheEntry tracks recency for eviction.
type cacheEntry struct {
	sess     *Session
	lastUsed time.Time
}

// NewStore opens (creating if needed) a session directory. cacheSize bounds
// in-memory sessions; others reload from disk on demand.
func NewStore(dir string, cacheSize int) (*Store, error) {
	if cacheSize < 1 {
		return nil, fmt.Errorf("session: cache size must be >= 1, got %d", cacheSize)
	}

	if err := os.MkdirAll(dir, 0o755); err != nil { //nolint:gosec // 0755 is intentional
		return nil, fmt.Errorf("session: creating dir: %w", err)
	}

	return &Store{dir: dir, maxCached: cacheSize, cache: make(map[string]*cacheEntry)}, nil
}

// Create starts a new session with a fresh ULID. The first JSONL line is a
// session_meta event so files are self-describing on replay.
func (s *Store) Create(cwd string) (*Session, error) {
	id := newID()
	created := ulidTime(id)

	meta := map[string]any{
		"type":    "session_meta",
		"session": id,
		"cwd":     cwd,
		"created": created.UTC().Format(time.RFC3339),
	}
	if err := s.Append(id, meta); err != nil {
		return nil, err
	}

	sess := &Session{ID: id, Cwd: cwd, Created: created}

	s.mu.Lock()
	defer s.mu.Unlock()

	s.cache[sess.ID] = &cacheEntry{sess: sess, lastUsed: time.Now()}
	s.evictLocked()

	return sess, nil
}

// RawEvents returns the stored JSONL lines verbatim (for history replay).
func (s *Store) RawEvents(id string) ([]json.RawMessage, error) {
	if !validID(id) {
		return nil, fmt.Errorf("session: invalid id %q", id)
	}

	path := s.path(id)

	f, err := os.Open(path) //nolint:gosec // id validated against ULID format
	if err != nil {
		return nil, fmt.Errorf("session: opening %s: %w", path, err)
	}
	defer f.Close() //nolint:errcheck // read-only handle

	var events []json.RawMessage

	scanner := bufio.NewScanner(f)
	scanner.Buffer(make([]byte, 0, 64*1024), 4*1024*1024)

	for scanner.Scan() {
		line := scanner.Bytes()
		lineCopy := make([]byte, len(line))
		copy(lineCopy, line)
		events = append(events, lineCopy)
	}

	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("session: scanning %s: %w", path, err)
	}

	return events, nil
}

// Invalidate drops a session from the cache; the next Get reloads and folds
// from disk (the storage file is the source of truth).
func (s *Store) Invalidate(id string) {
	if !validID(id) {
		return
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	delete(s.cache, id)
}

// Dir returns the directory backing the store (used by tests and tooling).
func (s *Store) Dir() string {
	return s.dir
}

// Get returns a session by id, loading and folding its JSONL file on cache
// miss.
func (s *Store) Get(id string) (*Session, error) {
	if !validID(id) {
		return nil, fmt.Errorf("session: invalid id %q", id)
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	if entry, ok := s.cache[id]; ok {
		entry.lastUsed = time.Now()
		return entry.sess, nil
	}

	sess, err := s.load(id)
	if err != nil {
		return nil, err
	}

	s.cache[id] = &cacheEntry{sess: sess, lastUsed: time.Now()}
	s.evictLocked()

	return sess, nil
}

// Append writes one event as a JSON line to the session file. The in-memory
// copy, if cached, is not modified here; ApplyMessage updates it.
func (s *Store) Append(id string, event any) error {
	if !validID(id) {
		return fmt.Errorf("session: invalid id %q", id)
	}

	line, err := json.Marshal(event)
	if err != nil {
		return fmt.Errorf("session: marshaling event: %w", err)
	}

	path := s.path(id)

	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644) //nolint:gosec // 0644 is intentional
	if err != nil {
		return fmt.Errorf("session: opening %s: %w", path, err)
	}
	defer f.Close() //nolint:errcheck // append-only handle

	if _, err := f.Write(append(line, '\n')); err != nil {
		return fmt.Errorf("session: appending to %s: %w", path, err)
	}

	return nil
}

// ApplyMessage appends messages to the cached session's folded history so
// subsequent turns see them without a disk reload.
func (s *Store) ApplyMessage(id string, msgs ...llm.Message) error {
	if !validID(id) {
		return fmt.Errorf("session: invalid id %q", id)
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	entry, ok := s.cache[id]
	if !ok {
		return nil // uncached sessions reload from disk next Get
	}

	entry.sess.Messages = append(entry.sess.Messages, msgs...)
	entry.lastUsed = time.Now()

	return nil
}

// FinishTurn increments the cached session's turn count.
func (s *Store) FinishTurn(id string) error {
	if !validID(id) {
		return fmt.Errorf("session: invalid id %q", id)
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	if entry, ok := s.cache[id]; ok {
		entry.sess.Turns++
		entry.lastUsed = time.Now()
	}

	return nil
}

// Summary describes one session for listings.
type Summary struct {
	ID         string
	Title      string
	Cwd        string
	Created    time.Time
	Turns      int
	LastActive time.Time
}

// List summarizes all sessions, most recently active first.
func (s *Store) List() ([]Summary, error) {
	entries, err := os.ReadDir(s.dir)
	if err != nil {
		return nil, fmt.Errorf("session: reading dir: %w", err)
	}

	var summaries []Summary

	for _, entry := range entries {
		name := entry.Name()
		if !strings.HasSuffix(name, fileSuffix) {
			continue
		}

		id := strings.TrimSuffix(name, fileSuffix)
		if !validID(id) {
			continue
		}

		sum, err := s.summarize(id)
		if err != nil {
			continue // unreadable sessions are skipped, not fatal
		}

		summaries = append(summaries, sum)
	}

	sort.Slice(summaries, func(i, j int) bool {
		return summaries[i].LastActive.After(summaries[j].LastActive)
	})

	return summaries, nil
}

// summarize builds one Summary from disk.
func (s *Store) summarize(id string) (Summary, error) {
	path := s.path(id)

	info, err := os.Stat(path)
	if err != nil {
		return Summary{}, fmt.Errorf("stating %s: %w", path, err)
	}

	sess, err := s.load(id)
	if err != nil {
		return Summary{}, err
	}

	return Summary{
		ID:         id,
		Title:      sess.Title,
		Cwd:        sess.Cwd,
		Created:    sess.Created,
		Turns:      sess.Turns,
		LastActive: info.ModTime(),
	}, nil
}

// load reads and folds a session file.
func (s *Store) load(id string) (*Session, error) {
	path := s.path(id)

	f, err := os.Open(path) //nolint:gosec // id validated against ULID format
	if err != nil {
		return nil, fmt.Errorf("session: opening %s: %w", path, err)
	}
	defer f.Close() //nolint:errcheck // read-only handle

	sess := &Session{ID: id, Cwd: s.sniffCwd(id), Created: ulidTime(id)}
	fold := newFolder()
	scanner := bufio.NewScanner(f)
	scanner.Buffer(make([]byte, 0, 64*1024), 4*1024*1024)

	for scanner.Scan() {
		fold.feed(scanner.Bytes())
	}

	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("session: scanning %s: %w", path, err)
	}

	sess.Messages = fold.history()
	sess.Turns = fold.turns
	sess.Title = fold.title

	return sess, nil
}

// sniffCwd recovers the recorded cwd. The first user_message line carries it
// is not guaranteed, so fall back to empty and let the first line's
// create marker win when present.
func (s *Store) sniffCwd(id string) string {
	path := s.path(id)

	f, err := os.Open(path) //nolint:gosec // id validated
	if err != nil {
		return ""
	}
	defer f.Close() //nolint:errcheck // read-only handle

	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		var meta struct {
			Type    string `json:"type"`
			Session string `json:"session"`
			Cwd     string `json:"cwd"`
		}
		if err := json.Unmarshal(scanner.Bytes(), &meta); err != nil {
			continue
		}

		if meta.Type == "session_meta" {
			return meta.Cwd
		}
	}

	return ""
}

// path is the JSONL file for a session id.
func (s *Store) path(id string) string {
	return filepath.Join(s.dir, id+fileSuffix)
}

// evictLocked drops least-recently-used sessions above the cache bound.
// Caller holds s.mu.
func (s *Store) evictLocked() {
	for len(s.cache) > s.maxCached {
		var (
			oldestID string
			oldest   time.Time
		)

		first := true
		for id, entry := range s.cache {
			if first || entry.lastUsed.Before(oldest) {
				oldestID, oldest, first = id, entry.lastUsed, false
			}
		}

		delete(s.cache, oldestID)
	}
}

// newID mints a ULID string. ULID construction with the default entropy
// source cannot fail.
func newID() string {
	return ulid.Make().String()
}

// maxInt64AsUint64 is the overflow guard boundary for ulidTime.
const maxInt64AsUint64 = uint64(1<<63 - 1)

// ulidTime extracts the embedded millisecond timestamp, saturating instead
// of overflowing on hostile values.
func ulidTime(id string) time.Time {
	parsed, err := ulid.Parse(id)
	if err != nil {
		return time.Time{}
	}

	ms := parsed.Time()
	if ms > maxInt64AsUint64 {
		return time.Time{}
	}

	return time.UnixMilli(int64(ms))
}

// ulidDigits is the Crockford base32 alphabet ULIDs use.
const ulidDigits = "0123456789ABCDEFGHJKMNPQRSTVWXYZ"

// validID rejects path-injecting ids: exactly 26 ULID characters.
func validID(id string) bool {
	if len(id) != 26 {
		return false
	}

	for _, c := range id {
		if !strings.ContainsRune(ulidDigits, c) {
			return false
		}
	}

	return true
}
