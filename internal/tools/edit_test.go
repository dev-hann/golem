package tools_test

import (
	"errors"
	"os"
	"strings"
	"testing"

	"github.com/dev-hann/golem/internal/tools"
)

func TestEdit_ReplacesUniqueString(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	writeFile(t, root, "main.go", "func main() {\n\tfmt.Println(\"hi\")\n}\n")
	tool := tools.EditTool{Root: root}
	_, err := tool.Execute(t.Context(), mustJSON(t, map[string]any{
		"path": "main.go", "old_string": "hi", "new_string": "hello",
	}))
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	assertFileContains(t, root, "main.go", "hello")
}

func TestEdit_ErrorsWhenNotFound(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	writeFile(t, root, "f.txt", "aaa\n")
	tool := tools.EditTool{Root: root}
	_, err := tool.Execute(t.Context(), mustJSON(t, map[string]any{
		"path": "f.txt", "old_string": "zzz", "new_string": "yyy",
	}))
	if !errors.Is(err, tools.ErrEditNotFound) {
		t.Errorf("err = %v, want ErrEditNotFound", err)
	}
}

func TestEdit_ErrorsWhenAmbiguous(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	writeFile(t, root, "f.txt", "x dup x dup x\n")
	tool := tools.EditTool{Root: root}
	_, err := tool.Execute(t.Context(), mustJSON(t, map[string]any{
		"path": "f.txt", "old_string": "dup", "new_string": "one",
	}))
	if !errors.Is(err, tools.ErrEditAmbiguous) {
		t.Errorf("err = %v, want ErrEditAmbiguous", err)
	}
}

func TestEdit_ReplaceAllReplacesEveryOccurrence(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	writeFile(t, root, "f.txt", "x dup x dup x\n")
	tool := tools.EditTool{Root: root}
	_, err := tool.Execute(t.Context(), mustJSON(t, map[string]any{
		"path": "f.txt", "old_string": "dup", "new_string": "one", "replace_all": true,
	}))
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	data := readFile(t, root, "f.txt")
	if got := strings.Count(data, "one"); got != 2 {
		t.Errorf("replacements = %d, want 2 (content: %q)", got, data)
	}
}

func TestEdit_RejectsIdenticalStrings(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	writeFile(t, root, "f.txt", "same\n")
	tool := tools.EditTool{Root: root}
	_, err := tool.Execute(t.Context(), mustJSON(t, map[string]any{
		"path": "f.txt", "old_string": "same", "new_string": "same",
	}))
	if err == nil || !strings.Contains(err.Error(), "identical") {
		t.Errorf("expected identical-strings error, got: %v", err)
	}
}

// assertFileContains fails when the file does not contain want.
func assertFileContains(t *testing.T, root, name, want string) {
	t.Helper()
	if !strings.Contains(readFile(t, root, name), want) {
		t.Errorf("%s should contain %q", name, want)
	}
}

// readFile reads a workspace file, failing the test on error.
func readFile(t *testing.T, root, name string) string {
	t.Helper()
	data, err := os.ReadFile(root + "/" + name)
	if err != nil {
		t.Fatalf("read %s: %v", name, err)
	}
	return string(data)
}
