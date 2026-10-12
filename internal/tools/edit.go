package tools

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strings"
)

// Edit errors. They are returned as-is so callers can branch on them with
// errors.Is; the agent converts them into error tool results the model can
// act on (retry with a corrected old_string).
var (
	// ErrEditNotFound means old_string does not occur in the file.
	ErrEditNotFound = errors.New("old string not found in file")
	// ErrEditAmbiguous means old_string occurs more than once.
	ErrEditAmbiguous = errors.New("old string matches multiple locations")
)

// EditTool applies an exact-string replacement in a file. This is the primary
// editing primitive: unambiguous, diff-friendly, and safe by construction.
type EditTool struct {
	// Root is the workspace directory relative paths resolve against.
	Root string
}

// editArgs are the model-facing arguments of EditTool.
type editArgs struct {
	Path       string `json:"path"`
	OldString  string `json:"old_string"`
	NewString  string `json:"new_string"`
	ReplaceAll bool   `json:"replace_all"`
}

// Name implements Tool.
func (t EditTool) Name() string { return "edit" }

// Description implements Tool.
func (EditTool) Description() string {
	return "Replace an exact string in a file. old_string must be unique unless replace_all is true."
}

// InputSchema implements Tool.
func (EditTool) InputSchema() map[string]any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"path":       map[string]any{"type": "string", "description": "File path, relative to the workspace root."},
			"old_string": map[string]any{"type": "string", "description": "Exact text to replace."},
			"new_string": map[string]any{"type": "string", "description": "Replacement text."},
			"replace_all": map[string]any{
				"type":        "boolean",
				"description": "Replace every occurrence instead of requiring uniqueness.",
			},
		},
		"required": []string{"path", "old_string", "new_string"},
	}
}

// Execute implements Tool.
func (t EditTool) Execute(_ context.Context, args json.RawMessage) (string, error) {
	var a editArgs
	if err := json.Unmarshal(args, &a); err != nil {
		return "", fmt.Errorf("edit: parsing args: %w", err)
	}

	if a.Path == "" {
		return "", fmt.Errorf("edit: missing path")
	}

	if a.OldString == a.NewString {
		return "", fmt.Errorf("edit: old_string and new_string are identical")
	}

	path, err := resolvePath(t.Root, a.Path)
	if err != nil {
		return "", fmt.Errorf("edit: %w", err)
	}

	data, err := os.ReadFile(path) //nolint:gosec // path is constrained to the workspace root
	if err != nil {
		return "", fmt.Errorf("edit %q: %w", a.Path, err)
	}

	content := string(data)

	count := strings.Count(content, a.OldString)
	switch {
	case count == 0:
		return "", fmt.Errorf("edit %q: %w", a.Path, ErrEditNotFound)
	case count > 1 && !a.ReplaceAll:
		return "", fmt.Errorf("edit %q: %d occurrences: %w", a.Path, count, ErrEditAmbiguous)
	}

	updated := strings.Replace(content, a.OldString, a.NewString, 1)
	if a.ReplaceAll {
		updated = strings.ReplaceAll(content, a.OldString, a.NewString)
	}

	if err := os.WriteFile(path, []byte(updated), 0o644); err != nil { //nolint:gosec // 0644 is intentional
		return "", fmt.Errorf("edit %q: %w", a.Path, err)
	}

	return fmt.Sprintf("replaced %d occurrence(s) in %s", countIfAll(count, a.ReplaceAll), a.Path), nil
}

// countIfAll reports the replacement count for the summary line.
func countIfAll(count int, replaceAll bool) int {
	if replaceAll {
		return count
	}

	return 1
}
