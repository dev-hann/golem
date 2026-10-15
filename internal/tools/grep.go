package tools

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

// maxGrepMatches bounds results so a pathological pattern cannot flood the
// model context before the byte budget even applies.
const maxGrepMatches = 200

// GrepTool regex-searches files under a path and returns "path:line:text"
// matches.
type GrepTool struct {
	// Root is the workspace directory relative paths resolve against.
	Root string
}

// grepArgs are the model-facing arguments of GrepTool.
type grepArgs struct {
	Pattern string `json:"pattern"`
	Path    string `json:"path"`
	Include string `json:"include"`
}

// Name implements Tool.
func (t GrepTool) Name() string { return "grep" }

// Description implements Tool.
func (GrepTool) Description() string {
	return "Regex-search file contents under a path. Returns path:line:text matches."
}

// InputSchema implements Tool.
func (GrepTool) InputSchema() map[string]any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"pattern": map[string]any{"type": "string", "description": "Regular expression."},
			"path":    map[string]any{"type": "string", "description": "Directory or file to search; defaults to the root."},
			"include": map[string]any{"type": "string", "description": "Optional glob filter on file names, e.g. *.go."},
		},
		"required": []string{"pattern"},
	}
}

// Execute implements Tool.
func (t GrepTool) Execute(_ context.Context, args json.RawMessage) (string, error) {
	var a grepArgs
	if err := json.Unmarshal(args, &a); err != nil {
		return "", fmt.Errorf("grep: parsing args: %w", err)
	}

	if a.Pattern == "" {
		return "", fmt.Errorf("grep: missing pattern")
	}

	rx, err := regexp.Compile(a.Pattern)
	if err != nil {
		return "", fmt.Errorf("grep: compiling pattern: %w", err)
	}

	start := a.Path
	if start == "" {
		start = "."
	}

	root, err := resolvePath(t.Root, ".")
	if err != nil {
		return "", fmt.Errorf("grep: %w", err)
	}

	target, err := resolvePath(t.Root, start)
	if err != nil {
		return "", fmt.Errorf("grep: %w", err)
	}

	matches, err := grepWalk(rx, target, a.Include, root, loadIgnoreSet(root))
	if err != nil {
		return "", fmt.Errorf("grep: %w", err)
	}

	if len(matches) == 0 {
		return "no matches", nil
	}

	return strings.Join(matches, "\n"), nil
}

// grepWalk walks target collecting matches. Skip-listed directories
// (node_modules, build output, VCS internals, …) and root-.gitignore paths
// are pruned (ADR-0033).
func grepWalk(rx *regexp.Regexp, target, include, root string, ign *ignoreSet) ([]string, error) {
	var matches []string

	emit := func(path string, line int, text string) {
		rel := relPath(root, path)
		matches = append(matches, fmt.Sprintf("%s:%d:%s", rel, line, text))
	}

	info, err := os.Stat(target)
	if err != nil {
		return nil, fmt.Errorf("stating %q: %w", target, err)
	}

	if !info.IsDir() {
		_ = grepFile(rx, target, include, emit)
		return matches, nil
	}

	walkErr := filepath.WalkDir(target, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}

		rel := relPath(root, path)

		if d.IsDir() {
			if path == root {
				return nil
			}

			if walkSkipDir(d.Name(), rel, ign) {
				return filepath.SkipDir
			}

			return nil
		}

		if walkSkipFile(rel, ign, false) {
			return nil
		}

		_ = grepFile(rx, path, include, func(p string, l int, s string) {
			if len(matches) < maxGrepMatches {
				emit(p, l, s)
			}
		})

		return nil // unreadable files are skipped, not fatal
	})
	if walkErr != nil {
		return nil, fmt.Errorf("walking %q: %w", target, walkErr)
	}

	return matches, nil
}

// grepFile scans one file, invoking emit for each matching line. Binary-ish
// content (by content sniffing, ADR-0033) is skipped.
func grepFile(
	rx *regexp.Regexp,
	path, include string,
	emit func(path string, line int, text string),
) error {
	if include != "" {
		matched, err := filepath.Match(include, filepath.Base(path))
		if err != nil || !matched {
			return err
		}
	}

	f, err := os.Open(path) //nolint:gosec // path comes from a workspace walk
	if err != nil {
		return err
	}
	defer f.Close() //nolint:errcheck // read-only handle

	reader := bufio.NewReader(f)

	probe, _ := reader.Peek(512)
	if isBinaryContent(probe) {
		return nil
	}

	lineNo := 0

	for {
		line, readErr := reader.ReadString('\n')
		if line != "" {
			lineNo++
			if rx.MatchString(strings.TrimRight(line, "\n")) {
				emit(path, lineNo, strings.TrimRight(line, "\n"))
			}
		}

		if readErr != nil {
			break
		}
	}

	return nil
}

// relPath renders path relative to root when possible.
func relPath(root, path string) string {
	rel, err := filepath.Rel(root, path)
	if err != nil {
		return path
	}

	return filepath.ToSlash(rel)
}
