package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"
)

// ReadTool reads a file and returns its content with 1-based line-number
// prefixes so the model can address ranges precisely.
type ReadTool struct {
	// Root is the workspace directory relative paths resolve against.
	Root string
}

// readArgs are the model-facing arguments of ReadTool.
type readArgs struct {
	Path   string `json:"path"`
	Offset int    `json:"offset"`
	Limit  int    `json:"limit"`
}

// Name implements Tool.
func (t ReadTool) Name() string { return "read" }

// Description implements Tool.
func (ReadTool) Description() string {
	return "Read a file and return its content with line-number prefixes (n: text). " +
		"Use offset/limit to read a line window of a large file instead of all of it. " +
		"Binary files are refused; inspect them with bash (e.g. strings, file) instead."
}

// InputSchema implements Tool.
func (ReadTool) InputSchema() map[string]any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"path": map[string]any{
				"type":        "string",
				"description": "File path, relative to the workspace root.",
			},
			"offset": map[string]any{
				"type":        "integer",
				"description": "First line to return, 1-based. Optional; defaults to 1.",
			},
			"limit": map[string]any{
				"type":        "integer",
				"description": "Number of lines to return. Optional; defaults to the rest of the file.",
			},
		},
		"required": []string{"path"},
	}
}

// Execute implements Tool.
func (t ReadTool) Execute(_ context.Context, args json.RawMessage) (string, error) {
	var a readArgs
	if err := json.Unmarshal(args, &a); err != nil {
		return "", fmt.Errorf("read: parsing args: %w", err)
	}

	if a.Path == "" {
		return "", fmt.Errorf("read: missing path")
	}

	if a.Offset < 0 {
		return "", fmt.Errorf("read: offset must be >= 0 (got %d)", a.Offset)
	}

	if a.Limit < 0 {
		return "", fmt.Errorf("read: limit must be >= 0 (got %d)", a.Limit)
	}

	path, err := resolvePath(t.Root, a.Path)
	if err != nil {
		return "", fmt.Errorf("read: %w", err)
	}

	data, err := os.ReadFile(path) //nolint:gosec // path is constrained to the workspace root
	if err != nil {
		return "", fmt.Errorf("read %q: %w", a.Path, err)
	}

	if isBinaryContent(data[:min(len(data), sniffLen)]) {
		return fmt.Sprintf(
			"%s looks binary — read refuses non-text files; inspect it with bash (file, strings) instead",
			a.Path,
		), nil
	}

	return withLineNumbers(string(data), a.Offset, a.Limit), nil
}

// withLineNumbers prefixes the selected lines with their 1-based numbers.
// offset 0 means "from the first line"; limit 0 means "to the end".
func withLineNumbers(content string, offset, limit int) string {
	lines := strings.Split(strings.TrimSuffix(content, "\n"), "\n")

	if offset > 0 && offset > len(lines) {
		return ""
	}

	if offset > 0 {
		lines = lines[offset-1:]
	}

	if limit > 0 && limit < len(lines) {
		lines = lines[:limit]
	}

	var b strings.Builder
	for i, line := range lines {
		fmt.Fprintf(&b, "%d: %s\n", startNumber(offset)+i, line)
	}

	return b.String()
}

// startNumber computes the 1-based line number the numbering starts at.
func startNumber(offset int) int {
	if offset <= 0 {
		return 1
	}

	return offset
}
