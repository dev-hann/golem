package tools_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/golem-agent/golem/internal/tools"
)

func TestWrite_CreatesNestedAndOverwrites(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	tool := tools.WriteTool{Root: root}
	tests := []struct {
		name    string
		path    string
		content string
	}{
		{name: "nested create", path: "a/b/new.txt", content: "first"},
		{name: "overwrite", path: "a/b/new.txt", content: "second"},
		{name: "top level", path: "top.txt", content: "top"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := tool.Execute(t.Context(), mustJSON(t,
				map[string]string{"path": tt.path, "content": tt.content}))
			if err != nil {
				t.Fatalf("Execute: %v", err)
			}
			if !strings.Contains(got, tt.path) {
				t.Errorf("summary should name path, got: %s", got)
			}
			data, err := os.ReadFile(filepath.Join(root, tt.path))
			if err != nil {
				t.Fatalf("reading back %s: %v", tt.path, err)
			}
			if string(data) != tt.content {
				t.Errorf("file content = %q, want %q", data, tt.content)
			}
		})
	}
}

func TestWrite_ErrorsOnMissingPath(t *testing.T) {
	t.Parallel()
	tool := tools.WriteTool{Root: t.TempDir()}
	_, err := tool.Execute(t.Context(), mustJSON(t, map[string]string{"content": "x"}))
	if err == nil || !strings.Contains(err.Error(), "missing path") {
		t.Errorf("expected missing-path error, got: %v", err)
	}
}
