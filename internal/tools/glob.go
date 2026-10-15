package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"io/fs"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
)

// maxGlobResults bounds the returned path list.
const maxGlobResults = 500

// GlobTool lists workspace files matching a glob pattern. Patterns support
// *, ?, and ** (cross-directory) segments, matched against forward-slash
// relative paths.
type GlobTool struct {
	// Root is the workspace directory the pattern resolves against.
	Root string
}

// globArgs are the model-facing arguments of GlobTool.
type globArgs struct {
	Pattern string `json:"pattern"`
}

// Name implements Tool.
func (t GlobTool) Name() string { return "glob" }

// Description implements Tool.
func (GlobTool) Description() string {
	return "List files matching a glob pattern (*, ?, **). Paths are workspace-relative. " +
		"Dependency/build directories and binary files are skipped; more results need a tighter pattern."
}

// InputSchema implements Tool.
func (GlobTool) InputSchema() map[string]any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"pattern": map[string]any{
				"type":        "string",
				"description": "Glob pattern, e.g. **/*.go or src/*.ts.",
			},
		},
		"required": []string{"pattern"},
	}
}

// Execute implements Tool.
func (t GlobTool) Execute(_ context.Context, args json.RawMessage) (string, error) {
	var a globArgs
	if err := json.Unmarshal(args, &a); err != nil {
		return "", fmt.Errorf("glob: parsing args: %w", err)
	}

	if a.Pattern == "" {
		return "", fmt.Errorf("glob: missing pattern")
	}

	rx, err := globToRegex(a.Pattern)
	if err != nil {
		return "", fmt.Errorf("glob: %w", err)
	}

	root, err := resolvePath(t.Root, ".")
	if err != nil {
		return "", fmt.Errorf("glob: %w", err)
	}

	ign := loadIgnoreSet(root)

	var results []string

	walkErr := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}

		if d.IsDir() {
			if path == root {
				return nil
			}

			if walkSkipDir(d.Name(), relPath(root, path), ign) {
				return filepath.SkipDir
			}

			return nil
		}

		if len(results) >= maxGlobResults {
			return filepath.SkipAll
		}

		rel := relPath(root, path)
		if walkSkipFile(rel, ign, true) {
			return nil
		}

		if rx.MatchString(rel) {
			results = append(results, rel)
		}

		return nil
	})
	if walkErr != nil {
		return "", fmt.Errorf("glob: walking %q: %w", root, walkErr)
	}

	if len(results) == 0 {
		return "no matches", nil
	}

	sort.Strings(results)

	return strings.Join(results, "\n"), nil
}

// globToRegex compiles a glob pattern into a regexp over slash-separated
// relative paths. ** matches across directory separators; * and ? do not.
func globToRegex(pattern string) (*regexp.Regexp, error) {
	var b strings.Builder
	b.WriteString("^")

	for i := 0; i < len(pattern); i++ {
		switch c := pattern[i]; c {
		case '*':
			if i+1 < len(pattern) && pattern[i+1] == '*' {
				b.WriteString(".*")

				i++
			} else {
				b.WriteString("[^/]*")
			}
		case '?':
			b.WriteString("[^/]")
		default:
			b.WriteString(regexp.QuoteMeta(string(c)))
		}
	}

	b.WriteString("$")

	return regexp.Compile(b.String())
}
