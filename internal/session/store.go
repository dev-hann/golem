// Package session persists conversations as append-only JSONL files and
// folds them back into conversation state (see docs/DECISIONS.md ADR-0008).
// The stored lines are the same wire events defined in docs/PROTOCOL.md.
package session

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"io"
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
	ID        string
	Cwd       string
	Title     string
	Created   time.Time
	Turns     int
	TokensIn  int64
	TokensOut int64
	Messages  []llm.Message

	// FirstUserText is the session's opening user message, used by the
	// server's title side call (and its retries — whichever turn they
	// follow). Empty when storage holds no user message.
	FirstUserText string
}

// Store is an append-only session store with a bounded LRU cache.
type Store struct {
	dir       string
	maxCached int

	mu        sync.Mutex
	cache     map[string]*cacheEntry
	summaries map[string]summaryInfo // List summaries keyed by session id
	deleted   map[string]struct{}    // deleted session ids; Append refuses them forever
}

// cacheEntry is the cached fold of one session: the live folder, the
// folded frontier (byte offset of the last complete line), and any
// unterminated trailing bytes. lastUsed drives eviction.
type cacheEntry struct {
	fold     *folder
	offset   int64
	tail     []byte // unterminated trailing bytes, folded into snapshots only
	lastUsed time.Time
}

// snapshot returns a Session view of the entry with any unterminated
// tail folded in. The folder is cloned first: building a view closes the
// trailing partial turn, which must not mutate the live folder (the tail
// refolds from the frontier once its newline lands).
func (e *cacheEntry) snapshot(id string) *Session {
	fold := e.fold.clone()
	if len(e.tail) > 0 {
		fold.feed(e.tail)
	}

	return sessionFromFolder(id, fold)
}

// sessionFromFolder builds the Session view of a folded file.
func sessionFromFolder(id string, fold *folder) *Session {
	return &Session{
		ID: id, Created: ulidTime(id),
		Cwd: fold.cwd, Messages: fold.history(),
		Turns: fold.turns, Title: fold.title, FirstUserText: fold.firstUserText,
		TokensIn: fold.tokensIn, TokensOut: fold.tokensOut,
	}
}

// ErrDeleted reports an Append to a deleted session: the write is
// refused instead of resurrecting the removed file.
var ErrDeleted = errors.New("session: session is deleted")

// NewStore opens (creating if needed) a session directory. cacheSize bounds
// in-memory sessions; others reload from disk on demand.
func NewStore(dir string, cacheSize int) (*Store, error) {
	if cacheSize < 1 {
		return nil, fmt.Errorf("session: cache size must be >= 1, got %d", cacheSize)
	}

	if err := os.MkdirAll(dir, 0o755); err != nil { //nolint:gosec // 0755 is intentional
		return nil, fmt.Errorf("session: creating dir: %w", err)
	}

	return &Store{
		dir: dir, maxCached: cacheSize,
		cache: make(map[string]*cacheEntry), summaries: make(map[string]summaryInfo),
		deleted: make(map[string]struct{}),
	}, nil
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

	fold := foldOfMeta(id, cwd)

	sess := sessionFromFolder(id, fold)

	s.mu.Lock()
	defer s.mu.Unlock()

	s.cache[sess.ID] = &cacheEntry{fold: fold, lastUsed: time.Now()}
	s.evictLocked()

	return sess, nil
}

// foldOfMeta is the folder carrying Create's just-written header line,
// built separately so Create stays a single locked section.
func foldOfMeta(id, cwd string) *folder {
	fold := newFolder()

	meta, err := json.Marshal(map[string]any{
		"type":    "session_meta",
		"session": id,
		"cwd":     cwd,
		"created": ulidTime(id).UTC().Format(time.RFC3339),
	})
	if err != nil {
		return fold
	}

	fold.feed(append(meta, '\n'))

	return fold
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
	delete(s.summaries, id)
}

// Delete removes a session outright: the cache entry and the JSONL file.
// It does not mutate records (ADR-0008 governs appends); removal is a
// whole-file operation, ADR-0019. The delete flag is permanent: once a
// session is deleted, Appends to its id fail with ErrDeleted forever —
// there is no window where a concurrent or late Append (a turn goroutine
// that outlived the drain bound, the title side call) can re-create the
// file. Both the flag write and the remove happen under the store lock,
// so an Append either lands before the remove (the file goes away with
// it) or fails the flag check — never resurrects.
func (s *Store) Delete(id string) error {
	if !validID(id) {
		return fmt.Errorf("session: invalid id %q", id)
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	delete(s.cache, id)
	delete(s.summaries, id)

	path := s.path(id)
	if err := os.Remove(path); err != nil {
		return fmt.Errorf("session: deleting %s: %w", path, err)
	}

	s.deleted[id] = struct{}{}

	return nil
}

// Dir returns the directory backing the store (used by tests and tooling).
func (s *Store) Dir() string {
	return s.dir
}

// Get returns a session by id, loading and folding its JSONL file on
// cache miss. On a hit the fold resumes from the entry's byte frontier,
// so the returned session always reflects every appended line
// (ADR-0029).
func (s *Store) Get(id string) (*Session, error) {
	if !validID(id) {
		return nil, fmt.Errorf("session: invalid id %q", id)
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	entry, ok := s.cache[id]
	if !ok {
		loaded, err := s.loadEntry(id)
		if err != nil {
			return nil, err
		}

		s.cache[id] = loaded
		s.evictLocked()

		entry = loaded
	}

	if err := s.resumeEntry(id, entry); err != nil {
		// The file changed shape under the cache (shrank or vanished);
		// fall back to a full reload.
		fresh, err := s.loadEntry(id)
		if err != nil {
			return nil, err
		}

		s.cache[id] = fresh
		entry = fresh
	}

	entry.lastUsed = time.Now()

	return entry.snapshot(id), nil
}

// resumeEntry folds every newly complete line since the entry's frontier,
// keeping Get fresh on cache hits (ADR-0029). The frontier advances past
// newlines only: an unterminated trailing line waits in entry.tail and
// refolds from the frontier once its newline lands, so a partially
// written line is never consumed twice. A file shorter than the frontier
// (external rewrite) reports os.ErrNotExist to force a reload.
func (s *Store) resumeEntry(id string, entry *cacheEntry) error {
	path := s.path(id)

	info, err := os.Stat(path)
	if err != nil {
		return fmt.Errorf("stating %s: %w", path, err)
	}

	if info.Size() < entry.offset {
		return fmt.Errorf("%s shrank from %d to %d bytes: %w",
			path, entry.offset, info.Size(), os.ErrNotExist)
	}

	if info.Size() == entry.offset {
		return nil // unchanged
	}

	f, err := os.Open(path) //nolint:gosec // id validated against ULID format
	if err != nil {
		return fmt.Errorf("opening %s: %w", path, err)
	}
	defer f.Close() //nolint:errcheck // read-only handle

	if _, err := f.Seek(entry.offset, io.SeekStart); err != nil {
		return fmt.Errorf("seeking %s to %d: %w", path, entry.offset, err)
	}

	tail, offset, err := foldCompleteLines(f, entry.fold)
	if err != nil {
		return err
	}

	entry.tail, entry.offset = tail, entry.offset+offset

	return nil
}

// maxLineBytes bounds one stored line while folding (the append-side
// scanner buffers use the same cap).
const maxLineBytes = 4 * 1024 * 1024

// foldCompleteLines feeds the folder every newline-terminated line from
// r and returns the trailing unterminated bytes plus the byte count of
// the complete lines consumed (the caller adds its base offset).
// Lines longer than maxLineBytes stop the fold with an error rather
// than growing without bound.
func foldCompleteLines(r io.Reader, fold *folder) (tail []byte, complete int64, err error) {
	reader := bufio.NewReaderSize(r, lineReadChunk)

	var completed int64

	for {
		line, err := reader.ReadBytes('\n')
		if len(line) > maxLineBytes {
			return nil, 0, fmt.Errorf("line exceeds %d bytes: %w", maxLineBytes, bufio.ErrBufferFull)
		}

		if err == nil {
			completed += int64(len(line))
			fold.feed(line)

			continue
		}

		if errors.Is(err, io.EOF) {
			tail := make([]byte, len(line))
			copy(tail, line)

			return tail, completed, nil
		}

		return nil, 0, fmt.Errorf("reading lines: %w", err)
	}
}

// lineReadChunk is the buffering chunk for line reads while folding.
const lineReadChunk = 64 * 1024

// Append writes one event as a JSON line to the session file. The in-memory
// copy, if cached, is not modified here; the cache is invalidated by the
// caller (the server) once the turn ends, so the next Get re-folds from
// disk. Appends to a session mid-delete fail with ErrDeleted — the file
// stays removed.
func (s *Store) Append(id string, event any) error {
	if !validID(id) {
		return fmt.Errorf("session: invalid id %q", id)
	}

	line, err := json.Marshal(event)
	if err != nil {
		return fmt.Errorf("session: marshaling event: %w", err)
	}

	// The whole check-open-write sequence holds the store lock: a
	// concurrent Delete's flag+remove cannot interleave between the
	// check and the O_CREATE — whichever side takes the lock second sees
	// the first's outcome (a flagged Append fails; an unflagged file is
	// removed together with any just-appended line).
	s.mu.Lock()
	defer s.mu.Unlock()

	if _, gone := s.deleted[id]; gone {
		return fmt.Errorf("session: appending to %s: %w", id, ErrDeleted)
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

// ContextBytes returns the byte size of the folded conversation as the
// model will see it: message text, tool-call argument payloads, and tool
// result outputs (ADR-0025 — the auto-compaction trigger measures the
// history a turn is about to replay).
func (s *Session) ContextBytes() int {
	total := 0
	for _, m := range s.Messages {
		total += len(m.Text)

		for _, call := range m.ToolCalls {
			total += len(call.Args)
		}

		for _, result := range m.ToolResults {
			total += len(result.Output)
		}
	}

	return total
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

// List summarizes all sessions, most recently active first. Summaries
// are cached per file (size+mtime), so refreshing an unchanged listing
// costs only directory stats.
func (s *Store) List() ([]Summary, error) {
	entries, err := os.ReadDir(s.dir)
	if err != nil {
		return nil, fmt.Errorf("session: reading dir: %w", err)
	}

	s.mu.Lock()
	defer s.mu.Unlock()

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

// summarize builds one Summary for a session file, caching by
// (size, mtime) so a refreshed listing does not refold unchanged files.
// The caller holds s.mu.
func (s *Store) summarize(id string) (Summary, error) {
	path := s.path(id)

	info, err := os.Stat(path)
	if err != nil {
		return Summary{}, fmt.Errorf("stating %s: %w", path, err)
	}

	if cached, ok := s.summaries[id]; ok && cached.size == info.Size() && cached.mtime.Equal(info.ModTime()) {
		return cached.summary, nil
	}

	entry, err := s.loadEntry(id)
	if err != nil {
		return Summary{}, err
	}

	sess := entry.snapshot(id)

	summary := Summary{
		ID:         id,
		Title:      sess.Title,
		Cwd:        sess.Cwd,
		Created:    sess.Created,
		Turns:      sess.Turns,
		LastActive: info.ModTime(),
	}

	s.summaries[id] = summaryInfo{summary: summary, size: info.Size(), mtime: info.ModTime()}

	return summary, nil
}

// summaryInfo is one cached List summary plus the file identity it was
// built from.
type summaryInfo struct {
	summary Summary
	size    int64
	mtime   time.Time
}

// loadEntry fully reads and folds a session file in one pass:
// conversation state and identity (cwd, from the session_meta header
// line) come out of the same scan.
func (s *Store) loadEntry(id string) (*cacheEntry, error) {
	path := s.path(id)

	f, err := os.Open(path) //nolint:gosec // id validated against ULID format
	if err != nil {
		return nil, fmt.Errorf("session: opening %s: %w", path, err)
	}
	defer f.Close() //nolint:errcheck // read-only handle

	fold := newFolder()

	tail, offset, err := foldCompleteLines(f, fold)
	if err != nil {
		return nil, err
	}

	return &cacheEntry{fold: fold, offset: offset, tail: tail, lastUsed: time.Now()}, nil
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
