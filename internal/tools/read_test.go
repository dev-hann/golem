package tools_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/golem-agent/golem/internal/tools"
)

func TestRead_ReturnsContentWithLineNumbers(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	writeFile(t, root, "notes.txt", "alpha\nbeta\ngamma\n")
	tool := tools.ReadTool{Root: root}
	got, err := tool.Execute(t.Context(), mustJSON(t, map[string]string{"path": "notes.txt"}))
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	want := "1: alpha\n2: beta\n3: gamma\n"
	if got != want {
		t.Errorf("got:\n%q\nwant:\n%q", got, want)
	}
}

func TestRead_ErrorsOnMissingFile(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	tool := tools.ReadTool{Root: root}
	_, err := tool.Execute(t.Context(), mustJSON(t, map[string]string{"path": "ghost.txt"}))
	if err == nil {
		t.Fatal("expected error for missing file, got nil")
	}
	if !strings.Contains(err.Error(), "ghost.txt") {
		t.Errorf("error should name the file, got: %v", err)
	}
}

func TestRead_ErrorsOnMissingPathArg(t *testing.T) {
	t.Parallel()
	tool := tools.ReadTool{Root: t.TempDir()}
	_, err := tool.Execute(t.Context(), mustJSON(t, map[string]string{}))
	if err == nil || !strings.Contains(err.Error(), "missing path") {
		t.Errorf("expected missing-path error, got: %v", err)
	}
}

// writeFile creates a file under root with content, failing the test on error.
func writeFile(t *testing.T, root, name, content string) {
	t.Helper()
	path := filepath.Join(root, name)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("mkdir for %s: %v", name, err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("write %s: %v", name, err)
	}
}
