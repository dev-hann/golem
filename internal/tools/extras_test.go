package tools_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/dev-hann/golem/internal/tools"
)

func TestRegistry_EmptyRegistryHasNoSpecs(t *testing.T) {
	t.Parallel()
	reg, err := tools.NewRegistry()
	if err != nil {
		t.Fatalf("NewRegistry: %v", err)
	}
	if len(reg.Specs()) != 0 {
		t.Errorf("specs = %v, want empty", reg.Specs())
	}
}

func TestBash_RunsInWorkspaceRoot(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	tool := tools.BashTool{Root: root}
	got, err := tool.Execute(t.Context(), mustJSON(t, map[string]string{"command": "pwd"}))
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if !strings.Contains(got, root) {
		t.Errorf("pwd = %q, want inside %q", got, root)
	}
}

func TestBash_StartErrorSurfaces(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "missing-parent"), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	tool := tools.BashTool{Root: filepath.Join(root, "missing-parent", "nope")}
	_, err := tool.Execute(t.Context(), mustJSON(t, map[string]string{"command": "true"}))
	if err == nil {
		t.Fatal("expected start error for bad root, got nil")
	}
}

func TestGrep_SingleFileTarget(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	writeFile(t, root, "a.txt", "alpha\nbeta\nalpha\n")
	tool := tools.GrepTool{Root: root}
	got, err := tool.Execute(t.Context(), mustJSON(t,
		map[string]string{"pattern": "alpha", "path": "a.txt"}))
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if got != "a.txt:1:alpha\na.txt:3:alpha" {
		t.Errorf("single-file grep = %q", got)
	}
}

func TestRead_EmptyFileYieldsSingleEmptyLine(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	writeFile(t, root, "empty.txt", "")
	tool := tools.ReadTool{Root: root}
	got, err := tool.Execute(t.Context(), mustJSON(t, map[string]string{"path": "empty.txt"}))
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if got != "1: \n" {
		t.Errorf("empty file read = %q, want %q", got, "1: \n")
	}
}

func TestGlob_QuestionMarkMatchesSingleChar(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	writeFile(t, root, "a1.txt", "x")
	writeFile(t, root, "a12.txt", "x")
	tool := tools.GlobTool{Root: root}
	got, err := tool.Execute(t.Context(), mustJSON(t, map[string]string{"pattern": "a?.txt"}))
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if strings.Contains(got, "a12.txt") || !strings.Contains(got, "a1.txt") {
		t.Errorf("? glob = %q", got)
	}
}
