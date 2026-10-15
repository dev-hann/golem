// Session fork: copying a parent file's raw lines through a turn
// boundary into a fresh child file. See ADR-0034.

package session

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// Fork errors, matched by the server with errors.Is.
var (
	// ErrForkEmpty reports a parent with no completed turn to fork from.
	ErrForkEmpty = errors.New("session: fork: no completed turn")
	// ErrForkTurn reports a through-turn id the parent never completed.
	ErrForkTurn = errors.New("session: fork: unknown turn")
)

// forkTitleSuffix captures the fork counter of a title: "X (fork #2)" →
// base "X", 2. Opencode's increment rule.
var forkTitleSuffix = regexp.MustCompile(`^(.+) \(fork #(\d+)\)$`)

// parentHeaderLines is the session_meta header every session file starts
// with; the child writes its own header instead of copying the parent's.
const parentHeaderLines = 1

// Fork copies the parent's stored lines through a turn boundary into a
// new child session and returns the child. throughTurn "" forks through
// the last completed turn; otherwise it must name a turn the parent
// completed (its turn_end is the boundary, inclusive). The parent file
// is never touched. Scan and write run under the store lock, so appends
// and deletes cannot interleave with the copy.
func (s *Store) Fork(parentID, throughTurn string) (*Session, error) {
	if !validID(parentID) {
		return nil, fmt.Errorf("session: invalid id %q", parentID)
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	if _, gone := s.deleted[parentID]; gone {
		return nil, fmt.Errorf("session: forking %s: %w", parentID, ErrDeleted)
	}

	scan, err := scanForkLines(s.path(parentID))
	if err != nil {
		return nil, err
	}

	boundary, turn, err := scan.boundary(throughTurn)
	if err != nil {
		return nil, err
	}

	title, titled := s.nextForkTitle(scan.title)

	childID := newID()

	return s.writeForkChild(childID, parentID, turn, scan.cwd, title, titled, boundary)
}

// forkScan is what one pass over a parent file learns for a fork: the
// header's cwd, the last session_title record's title, and every
// completed turn's turn_end line index.
type forkScan struct {
	cwd      string
	title    string
	lastEnd  int
	lastTurn string
	turnEnd  map[string]int
}

// boundary resolves the copy boundary: the named turn's turn_end when
// throughTurn is set, else the last completed one.
func (fs *forkScan) boundary(throughTurn string) (lineIdx int, turn string, err error) {
	if throughTurn != "" {
		idx, ok := fs.turnEnd[throughTurn]
		if !ok {
			return 0, "", fmt.Errorf("%w: %s", ErrForkTurn, throughTurn)
		}

		return idx, throughTurn, nil
	}

	if fs.lastEnd < 0 {
		return 0, "", ErrForkEmpty
	}

	return fs.lastEnd, fs.lastTurn, nil
}

// scanForkLines reads a session file once, folding nothing but tracking
// the fields a fork needs. Only newline-terminated lines count (a crash
// tail cannot hold a turn_end).
func scanForkLines(path string) (*forkScan, error) {
	f, err := os.Open(path) //nolint:gosec // id validated against ULID format
	if err != nil {
		return nil, fmt.Errorf("session: opening %s: %w", path, err)
	}
	defer f.Close() //nolint:errcheck // read-only handle

	scan := &forkScan{lastEnd: -1, turnEnd: make(map[string]int)}

	r := bufio.NewReader(f)
	idx := 0

	for {
		line, err := r.ReadBytes('\n')

		if len(line) > 0 && line[len(line)-1] == '\n' {
			scan.feed(idx, line)
			idx++
		}

		if err != nil {
			return scan, nil // EOF or a torn tail: stop at the last complete line
		}
	}
}

// feed folds one complete raw line into the scan.
func (fs *forkScan) feed(idx int, line []byte) {
	var env struct {
		Type  string `json:"type"`
		Turn  string `json:"turn"`
		Cwd   string `json:"cwd"`
		Title string `json:"title"`
	}
	if err := json.Unmarshal(line, &env); err != nil {
		return // malformed lines are skipped, not fatal
	}

	switch env.Type {
	case "session_meta":
		if fs.cwd == "" { // first wins, matching the fold
			fs.cwd = env.Cwd
		}
	case "session_title":
		fs.title = env.Title // last wins, matching the fold
	case "turn_end":
		if env.Turn != "" {
			fs.turnEnd[env.Turn] = idx
			fs.lastEnd = idx
			fs.lastTurn = env.Turn
		}
	default:
		// other event types do not move the boundary
	}
}

// nextForkTitle derives the child's fork title from the parent's folded
// title: the parent's own suffix is stripped first, then 1 + the max
// suffix among existing sessions sharing that base (opencode's rule —
// forks of forks stay shallow). An untitled parent yields an untitled
// child. The caller holds s.mu.
func (s *Store) nextForkTitle(parentTitle string) (string, bool) {
	if parentTitle == "" {
		return "", false
	}

	base := parentTitle
	if m := forkTitleSuffix.FindStringSubmatch(parentTitle); m != nil {
		base = m[1]
	}

	maxFork := 0

	entries, err := os.ReadDir(s.dir)
	if err != nil {
		return base + " (fork #1)", true // unreadable dir: the common case still works
	}

	for _, entry := range entries {
		id := strings.TrimSuffix(entry.Name(), fileSuffix)
		if !validID(id) {
			continue
		}

		sum, err := s.summarize(id)
		if err != nil {
			continue // unreadable sessions are skipped, not fatal
		}

		if n, ok := forkSuffixOf(sum.Title, base); ok && n > maxFork {
			maxFork = n
		}
	}

	return base + " (fork #" + strconv.Itoa(maxFork+1) + ")", true
}

// forkSuffixOf reports whether title is the base itself or one of its
// forks, returning the fork number (0 for the plain base).
func forkSuffixOf(title, base string) (int, bool) {
	if title == base {
		return 0, true
	}

	if m := forkTitleSuffix.FindStringSubmatch(title); len(m) == 3 && m[1] == base {
		if n, err := strconv.Atoi(m[2]); err == nil {
			return n, true
		}
	}

	return 0, false
}

// writeForkChild writes the child file in one pass — meta header,
// lineage record, the parent's copied lines through the boundary with
// the session field rewritten, and the fork title — folding every line
// as it lands so the child enters the cache warm, and returns the
// child's folded view.
func (s *Store) writeForkChild(
	childID, parentID, throughTurn, cwd, title string,
	titled bool,
	boundary int,
) (*Session, error) {
	src, err := os.Open(s.path(parentID)) //nolint:gosec // id validated against ULID format
	if err != nil {
		return nil, fmt.Errorf("session: opening %s: %w", s.path(parentID), err)
	}
	defer src.Close() //nolint:errcheck // read-only handle

	childPath := s.path(childID)

	dst, err := os.OpenFile(childPath, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o644) //nolint:gosec // 0644 is intentional
	if err != nil {
		return nil, fmt.Errorf("session: creating %s: %w", childPath, err)
	}

	w := bufio.NewWriter(dst)
	fold := newFolder()

	committed := false

	defer func() {
		if !committed {
			_ = w.Flush() // best-effort: the child is discarded anyway
			_ = dst.Close()
			_ = os.Remove(childPath) // a half-written child must not linger
		}
	}()

	emit := func(ev map[string]any) error {
		line, err := json.Marshal(ev)
		if err != nil {
			return fmt.Errorf("session: marshaling fork line: %w", err)
		}

		if _, err := w.Write(append(line, '\n')); err != nil {
			return fmt.Errorf("session: writing %s: %w", childPath, err)
		}

		fold.feed(append(line, '\n'))

		return nil
	}

	if err := emit(map[string]any{
		"type": "session_meta", "session": childID,
		"cwd": cwd, "created": ulidTime(childID).UTC().Format(time.RFC3339),
	}); err != nil {
		return nil, err
	}

	if err := emit(map[string]any{
		"type": "session_forked", "session": childID,
		"parent": parentID, "through_turn": throughTurn,
	}); err != nil {
		return nil, err
	}

	if err := copyForkLines(w, fold, src, childID, boundary); err != nil {
		return nil, err
	}

	if titled {
		if err := emit(map[string]any{
			"type": "session_title", "session": childID, "title": title,
		}); err != nil {
			return nil, err
		}
	}

	if err := w.Flush(); err != nil {
		return nil, fmt.Errorf("session: flushing %s: %w", childPath, err)
	}

	if err := dst.Close(); err != nil {
		return nil, fmt.Errorf("session: closing %s: %w", childPath, err)
	}

	committed = true

	s.cache[childID] = &cacheEntry{fold: fold, lastUsed: time.Now()}
	s.evictLocked()

	return sessionFromFolder(childID, fold), nil
}

// copyForkLines streams the parent's lines 0..boundary to the child,
// skipping its meta header, rewriting each line's session field to the
// child id, and folding the result.
func copyForkLines(w *bufio.Writer, fold *folder, src *os.File, childID string, boundary int) error {
	r := bufio.NewReader(src)
	idx := 0

	for idx <= boundary {
		line, err := r.ReadBytes('\n')
		if err != nil || len(line) == 0 || line[len(line)-1] != '\n' {
			return fmt.Errorf("session: fork: parent line %d vanished mid-copy", idx)
		}

		if idx >= parentHeaderLines {
			rewritten, rerr := rewriteSession(line, childID)
			if rerr != nil {
				return rerr
			}

			if _, werr := w.Write(rewritten); werr != nil {
				return fmt.Errorf("session: writing fork copy: %w", werr)
			}

			fold.feed(rewritten)
		}

		idx++
	}

	return nil
}

// rewriteSession replaces one raw line's session field, preserving every
// other value byte-for-byte (values stay json.RawMessage; Go's map
// marshal is key-sorted, so the output is deterministic).
func rewriteSession(line []byte, childID string) ([]byte, error) {
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(line, &fields); err != nil {
		return nil, fmt.Errorf("session: fork: parsing line: %w", err)
	}

	id, err := json.Marshal(childID)
	if err != nil {
		return nil, fmt.Errorf("session: fork: marshaling id: %w", err)
	}

	fields["session"] = id

	out, err := json.Marshal(fields)
	if err != nil {
		return nil, fmt.Errorf("session: fork: rewriting line: %w", err)
	}

	return append(out, '\n'), nil
}
