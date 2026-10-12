package tools_test

import (
	"strings"
	"testing"

	"github.com/dev-hann/golem/internal/tools"
)

func TestGrep_MatchesPatternReturnsFileLineResults(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	writeFile(t, root, "a/one.go", "package a\n\nfunc Hello() {}\n")
	writeFile(t, root, "a/two.go", "package a\n\nfunc Bye() {}\n")
	writeFile(t, root, "b/.git/config", "func Hello() { hidden }\n")
	tool := tools.GrepTool{Root: root}
	got, err := tool.Execute(t.Context(), mustJSON(t,
		map[string]string{"pattern": `func (Hello|Bye)`}))
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if !strings.Contains(got, "a/one.go:3:func Hello() {}") {
		t.Errorf("missing one.go match: %q", got)
	}
	if !strings.Contains(got, "a/two.go:3:func Bye() {}") {
		t.Errorf("missing two.go match: %q", got)
	}
	if strings.Contains(got, ".git") {
		t.Errorf(".git should be skipped: %q", got)
	}
}

func TestGrep_IncludeFiltersFiles(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	writeFile(t, root, "x/keep.go", "needle\n")
	writeFile(t, root, "x/skip.txt", "needle\n")
	tool := tools.GrepTool{Root: root}
	got, err := tool.Execute(t.Context(), mustJSON(t,
		map[string]string{"pattern": "needle", "include": "*.go"}))
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if strings.Contains(got, "skip.txt") || !strings.Contains(got, "keep.go") {
		t.Errorf("include filter failed: %q", got)
	}
}

func TestGrep_NoMatchesReportsNone(t *testing.T) {
	t.Parallel()
	tool := tools.GrepTool{Root: t.TempDir()}
	got, err := tool.Execute(t.Context(), mustJSON(t, map[string]string{"pattern": "nothing"}))
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if got != "no matches" {
		t.Errorf("got %q, want no matches", got)
	}
}

func TestGrep_InvalidRegexErrors(t *testing.T) {
	t.Parallel()
	tool := tools.GrepTool{Root: t.TempDir()}
	_, err := tool.Execute(t.Context(), mustJSON(t, map[string]string{"pattern": "("}))
	if err == nil || !strings.Contains(err.Error(), "compiling pattern") {
		t.Errorf("expected compile error, got: %v", err)
	}
}

func TestGlob_MatchesRecursivePatterns(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	writeFile(t, root, "top.md", "x")
	writeFile(t, root, "src/a/deep/nested.go", "package a")
	writeFile(t, root, "src/b/flat.go", "package b")
	tool := tools.GlobTool{Root: root}
	got, err := tool.Execute(t.Context(), mustJSON(t, map[string]string{"pattern": "**/*.go"}))
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if !strings.Contains(got, "src/a/deep/nested.go") || !strings.Contains(got, "src/b/flat.go") {
		t.Errorf("recursive glob missed files: %q", got)
	}
	if strings.Contains(got, "top.md") {
		t.Errorf("non-matching file included: %q", got)
	}
}

func TestGlob_SingleStarDoesNotCrossDirectories(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	writeFile(t, root, "src/one.go", "x")
	writeFile(t, root, "src/sub/two.go", "x")
	tool := tools.GlobTool{Root: root}
	got, err := tool.Execute(t.Context(), mustJSON(t, map[string]string{"pattern": "*.go"}))
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if strings.Contains(got, "sub/two.go") {
		t.Errorf("* should not cross directories: %q", got)
	}
}

func TestGlob_NoMatchesReportsNone(t *testing.T) {
	t.Parallel()
	tool := tools.GlobTool{Root: t.TempDir()}
	got, err := tool.Execute(t.Context(), mustJSON(t, map[string]string{"pattern": "*.zzz"}))
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if got != "no matches" {
		t.Errorf("got %q, want no matches", got)
	}
}
