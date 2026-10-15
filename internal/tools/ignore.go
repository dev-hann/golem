package tools

import (
	"bufio"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

// sniffLen is the byte window used to decide whether content is binary.
const sniffLen = 512

// skipDirNames are directory names grep/glob walks never enter: VCS
// internals, dependency trees, build output, caches, and golem's own
// worktrees (ADR-0033). Peer agents ship equivalent defaults (Gemini CLI's
// COMMON_IGNORE_PATTERNS / COMMON_DIRECTORY_EXCLUDES).
var skipDirNames = map[string]struct{}{
	".git":             {},
	".hg":              {},
	".svn":             {},
	"node_modules":     {},
	"vendor":           {},
	"bower_components": {},
	"target":           {},
	"dist":             {},
	"build":            {},
	"bin":              {},
	"out":              {},
	"coverage":         {},
	"__pycache__":      {},
	".venv":            {},
	"venv":             {},
	".next":            {},
	".idea":            {},
	".vscode":          {},
	".worktrees":       {},
}

// binaryExtensions are file extensions glob refuses to list: read rejects
// binary content, so listing these paths is pure noise (ADR-0033). Mirrors
// Gemini CLI's BINARY_FILE_PATTERNS plus common media/archives.
var binaryExtensions = map[string]struct{}{
	".png": {}, ".jpg": {}, ".jpeg": {}, ".gif": {}, ".webp": {}, ".bmp": {},
	".ico": {}, ".pdf": {}, ".zip": {}, ".tar": {}, ".gz": {}, ".tgz": {},
	".bz2": {}, ".xz": {}, ".rar": {}, ".7z": {}, ".exe": {}, ".dll": {},
	".so": {}, ".dylib": {}, ".class": {}, ".jar": {}, ".war": {},
	".bin": {}, ".wasm": {}, ".o": {}, ".a": {}, ".lib": {}, ".pyc": {},
	".pyo": {}, ".swp": {}, ".ds_store": {}, ".woff": {}, ".woff2": {},
	".ttf": {}, ".otf": {}, ".mp3": {}, ".mp4": {}, ".mov": {}, ".sqlite": {},
}

// isBinaryContent reports whether the leading bytes of a file mark it as
// non-text. http.DetectContentType (the WHATWG sniffing algorithm) also
// recognizes UTF-16/32 BOMs as text, which a raw NUL probe misclassifies.
func isBinaryContent(head []byte) bool {
	return !strings.HasPrefix(http.DetectContentType(head), "text/")
}

// walkSkipDir reports whether a walk should prune the directory entry named
// name at workspace-relative path rel.
func walkSkipDir(name, rel string, ign *ignoreSet) bool {
	if _, ok := skipDirNames[name]; ok {
		return true
	}

	return ign.matches(rel, true)
}

// walkSkipFile reports whether a walk should omit the file at rel. The
// extension blocklist applies only to glob; grep opens files anyway and
// sniffs content instead (ADR-0033).
func walkSkipFile(rel string, ign *ignoreSet, byExtension bool) bool {
	if ign.matches(rel, false) {
		return true
	}

	if !byExtension {
		return false
	}

	ext := strings.ToLower(filepath.Ext(rel))
	_, blocked := binaryExtensions[ext]

	return blocked
}

// ignoreRule is one parsed .gitignore line: one regex per path segment.
type ignoreRule struct {
	segments []*regexp.Regexp
	dirOnly  bool // pattern carried a trailing slash
	anchored bool // pattern named a path from the workspace root
}

// ignoreSet is the parsed root .gitignore of a workspace. Deliberate
// simplifications (ADR-0033): only the root file is read, !negation and
// interior ** segments are unsupported (such lines are skipped).
type ignoreSet struct {
	rules []ignoreRule
}

// loadIgnoreSet parses root/.gitignore. A missing file yields an empty set;
// unparseable lines are skipped, matching how forgiving git tooling is.
func loadIgnoreSet(root string) *ignoreSet {
	set := &ignoreSet{}

	f, err := os.Open(filepath.Join(root, ".gitignore")) //nolint:gosec // fixed filename
	if err != nil {
		return set
	}
	defer f.Close() //nolint:errcheck // read-only handle

	sc := bufio.NewScanner(f)
	for sc.Scan() {
		if rule, ok := parseIgnoreLine(sc.Text()); ok {
			set.rules = append(set.rules, rule)
		}
	}

	return set
}

// matches reports whether rel (slash-separated, root-relative) is ignored.
// isDir distinguishes directory rules; files under a matched directory are
// never visited in the first place because the walk prunes the directory.
func (s *ignoreSet) matches(rel string, isDir bool) bool {
	if s == nil || len(s.rules) == 0 || rel == "" {
		return false
	}

	segs := strings.Split(strings.Trim(filepath.ToSlash(rel), "/"), "/")
	for _, r := range s.rules {
		if r.matches(segs, isDir) {
			return true
		}
	}

	return false
}

// matches evaluates one rule against path segments.
func (r *ignoreRule) matches(segs []string, isDir bool) bool {
	if r.dirOnly && !isDir {
		return false
	}

	if !r.anchored {
		// Unanchored rules carry a single segment and match any basename.
		return r.segments[0].MatchString(segs[len(segs)-1])
	}

	if len(segs) < len(r.segments) {
		return false
	}

	if !r.dirOnly && len(segs) != len(r.segments) {
		return false
	}

	for i, seg := range r.segments {
		if !seg.MatchString(segs[i]) {
			return false
		}
	}

	return true
}

// parseIgnoreLine turns one .gitignore line into a rule, reporting whether
// the line carried one. Blank lines, comments, negation, and interior **
// are not supported and are skipped (ADR-0033).
func parseIgnoreLine(line string) (ignoreRule, bool) {
	line = strings.TrimRight(line, " \t\r")
	if line == "" || strings.HasPrefix(line, "#") || strings.HasPrefix(line, "!") {
		return ignoreRule{}, false
	}

	rule := ignoreRule{dirOnly: strings.HasSuffix(line, "/")}
	line = strings.TrimSuffix(line, "/")

	// A leading "**/" is the unanchored form; a leading "/" anchors at root.
	line = strings.TrimPrefix(line, "**/")

	anchored := strings.Contains(line, "/")

	line = strings.TrimPrefix(line, "/")
	if strings.Contains(line, "**") {
		return ignoreRule{}, false
	}

	for _, seg := range strings.Split(line, "/") {
		rx, ok := compileGlobSegment(seg)
		if !ok {
			return ignoreRule{}, false
		}

		rule.segments = append(rule.segments, rx)
	}

	rule.anchored = anchored

	return rule, len(rule.segments) > 0
}

// compileGlobSegment compiles one path segment (* and ?, no crossing "/")
// into an anchored regex.
func compileGlobSegment(seg string) (*regexp.Regexp, bool) {
	var b strings.Builder
	b.WriteString("^")

	for i := range len(seg) {
		switch c := seg[i]; c {
		case '*':
			b.WriteString("[^/]*")
		case '?':
			b.WriteString("[^/]")
		default:
			b.WriteString(regexp.QuoteMeta(string(c)))
		}
	}

	b.WriteString("$")

	rx, err := regexp.Compile(b.String())

	return rx, err == nil
}
