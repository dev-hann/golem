package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
)

// WriteTool creates or overwrites a file with the given content, creating
// parent directories as needed.
type WriteTool struct {
	// Root is the workspace directory relative paths resolve against.
	Root string
}

// writeArgs are the model-facing arguments of WriteTool.
type writeArgs struct {
	Path    string `json:"path"`
	Content string `json:"content"`
}

// Name implements Tool.
func (t WriteTool) Name() string { return "write" }

// Description implements Tool.
func (WriteTool) Description() string {
	return "Create or overwrite a file with the given content. Parent directories are created."
}

// InputSchema implements Tool.
func (WriteTool) InputSchema() map[string]any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"path": map[string]any{
				"type":        "string",
				"description": "File path, relative to the workspace root.",
			},
			"content": map[string]any{
				"type":        "string",
				"description": "Full file content to write.",
			},
		},
		"required": []string{"path", "content"},
	}
}

// Execute implements Tool.
func (t WriteTool) Execute(_ context.Context, args json.RawMessage) (string, error) {
	var a writeArgs
	if err := json.Unmarshal(args, &a); err != nil {
		return "", fmt.Errorf("write: parsing args: %w", err)
	}

	if a.Path == "" {
		return "", fmt.Errorf("write: missing path")
	}

	path, err := resolvePath(t.Root, a.Path)
	if err != nil {
		return "", fmt.Errorf("write: %w", err)
	}

	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil { //nolint:gosec // 0755 is intentional
		return "", fmt.Errorf("write %q: creating parent dir: %w", a.Path, err)
	}

	if err := os.WriteFile(path, []byte(a.Content), 0o644); err != nil { //nolint:gosec // 0644 is intentional
		return "", fmt.Errorf("write %q: %w", a.Path, err)
	}

	return fmt.Sprintf("wrote %d bytes to %s", len(a.Content), a.Path), nil
}
