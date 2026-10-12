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
	Path string `json:"path"`
}

// Name implements Tool.
func (t ReadTool) Name() string { return "read" }

// Description implements Tool.
func (ReadTool) Description() string {
	return "Read a file and return its content with line-number prefixes (n: text)."
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

	path, err := resolvePath(t.Root, a.Path)
	if err != nil {
		return "", fmt.Errorf("read: %w", err)
	}

	data, err := os.ReadFile(path) //nolint:gosec // path is constrained to the workspace root
	if err != nil {
		return "", fmt.Errorf("read %q: %w", a.Path, err)
	}

	return withLineNumbers(string(data)), nil
}

// withLineNumbers prefixes every line with its 1-based number.
func withLineNumbers(content string) string {
	lines := strings.Split(strings.TrimSuffix(content, "\n"), "\n")

	var b strings.Builder
	for i, line := range lines {
		fmt.Fprintf(&b, "%d: %s\n", i+1, line)
	}

	return b.String()
}
