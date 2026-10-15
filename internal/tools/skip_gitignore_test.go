package tools_test

import (
	"strings"
	"testing"

	"github.com/dev-hann/golem/internal/tools"
)

// The skip list mirrors peer agents' default exclusions (Gemini CLI's
// COMMON_IGNORE_PATTERNS / COMMON_DIRECTORY_EXCLUDES): dependency trees and
// build output are never what a code-search question is about, and scanning
// them floods results with noise.
func TestGrep_SkipsDependencyAndBuildDirectories(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	writeFile(t, root, "src/main.go", "needle\n")
	skipDirs := []string{
		"node_modules", "vendor", "target", "dist", "build", "bin",
		".worktrees", ".venv", "__pycache__", ".next", ".idea", ".vscode",
	}
	for _, dir := range skipDirs {
		writeFile(t, root, dir+"/junk.go", "needle\n")
	}
	tool := tools.GrepTool{Root: root}
	got, err := tool.Execute(t.Context(), mustJSON(t, map[string]string{"pattern": "needle"}))
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if !strings.Contains(got, "src/main.go") {
		t.Errorf("real source missing from results: %q", got)
	}
	for _, dir := range skipDirs {
		if strings.Contains(got, dir+"/") {
			t.Errorf("skip-listed dir %q leaked into results: %q", dir, got)
		}
	}
}

func TestGlob_SkipsDependencyAndBuildDirectories(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	writeFile(t, root, "src/main.go", "x")
	skipDirs := []string{"node_modules", "vendor", "dist", "build", ".worktrees"}
	for _, dir := range skipDirs {
		writeFile(t, root, dir+"/junk.go", "x")
	}
	tool := tools.GlobTool{Root: root}
	got, err := tool.Execute(t.Context(), mustJSON(t, map[string]string{"pattern": "**/*.go"}))
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if !strings.Contains(got, "src/main.go") {
		t.Errorf("real source missing from results: %q", got)
	}
	for _, dir := range skipDirs {
		if strings.Contains(got, dir+"/") {
			t.Errorf("skip-listed dir %q leaked into results: %q", dir, got)
		}
	}
}

func TestGlob_SkipsBinaryExtensions(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	writeFile(t, root, "img.png", "\x89PNG\r\n\x1a\n")
	writeFile(t, root, "app.go", "package main")
	writeFile(t, root, "lib.so", "\x7fELF")
	tool := tools.GlobTool{Root: root}
	got, err := tool.Execute(t.Context(), mustJSON(t, map[string]string{"pattern": "*"}))
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if !strings.Contains(got, "app.go") {
		t.Errorf("source file missing: %q", got)
	}
	if strings.Contains(got, "img.png") || strings.Contains(got, "lib.so") {
		t.Errorf("binary files leaked into glob results: %q", got)
	}
}

func TestGrep_HonorsGitignore(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	writeFile(t, root, ".gitignore", "generated/\n*.gen.go\n")
	writeFile(t, root, "src/main.go", "needle\n")
	writeFile(t, root, "generated/out.go", "needle\n")
	writeFile(t, root, "src/thing.gen.go", "needle\n")
	tool := tools.GrepTool{Root: root}
	got, err := tool.Execute(t.Context(), mustJSON(t, map[string]string{"pattern": "needle"}))
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if !strings.Contains(got, "src/main.go") {
		t.Errorf("tracked file missing: %q", got)
	}
	for _, leaked := range []string{"generated/", "thing.gen.go"} {
		if strings.Contains(got, leaked) {
			t.Errorf("gitignored path %q leaked: %q", leaked, got)
		}
	}
}

func TestGlob_HonorsGitignore(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	writeFile(t, root, ".gitignore", "generated/\n*.gen.go\n")
	writeFile(t, root, "src/main.go", "x")
	writeFile(t, root, "generated/out.go", "x")
	writeFile(t, root, "src/thing.gen.go", "x")
	tool := tools.GlobTool{Root: root}
	got, err := tool.Execute(t.Context(), mustJSON(t, map[string]string{"pattern": "**/*.go"}))
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if !strings.Contains(got, "src/main.go") {
		t.Errorf("tracked file missing: %q", got)
	}
	if strings.Contains(got, "generated/") || strings.Contains(got, "thing.gen.go") {
		t.Errorf("gitignored path leaked: %q", got)
	}
}

func TestRead_OffsetLimitWindows(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	var b strings.Builder
	for i := 1; i <= 100; i++ {
		b.WriteString(strings.Repeat("x", i%10+1) + "\n")
	}
	writeFile(t, root, "big.txt", b.String())
	tool := tools.ReadTool{Root: root}
	got, err := tool.Execute(t.Context(), mustJSON(t, map[string]any{
		"path": "big.txt", "offset": 30, "limit": 10,
	}))
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	lines := strings.Split(strings.TrimRight(got, "\n"), "\n")
	if len(lines) != 10 {
		t.Errorf("want 10 lines, got %d: %q", len(lines), got)
	}
	if !strings.HasPrefix(lines[0], "30: ") {
		t.Errorf("window should start at line 30: %q", lines[0])
	}
	if !strings.HasPrefix(lines[9], "39: ") {
		t.Errorf("window should end at line 39: %q", lines[9])
	}
}

func TestRead_OffsetBeyondEofReturnsEmptyWindow(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	writeFile(t, root, "small.txt", "one\ntwo\n")
	tool := tools.ReadTool{Root: root}
	got, err := tool.Execute(t.Context(), mustJSON(t, map[string]any{
		"path": "small.txt", "offset": 50,
	}))
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if strings.TrimSpace(got) != "" {
		t.Errorf("expected empty window past EOF, got %q", got)
	}
}

func TestRead_NegativeOffsetErrors(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	writeFile(t, root, "f.txt", "a\nb\n")
	tool := tools.ReadTool{Root: root}
	_, err := tool.Execute(t.Context(), mustJSON(t, map[string]any{
		"path": "f.txt", "offset": -1,
	}))
	if err == nil || !strings.Contains(err.Error(), "offset") {
		t.Errorf("negative offset should error, got: %v", err)
	}
}

// offset 0 and limit 0 are indistinguishable from omitted in JSON, so they
// must mean the defaults: from the first line, to the end. limit alone
// reads the first N lines.
func TestRead_ZeroValuesMeanDefaults(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	writeFile(t, root, "f.txt", "one\ntwo\nthree\n")
	tool := tools.ReadTool{Root: root}
	got, err := tool.Execute(t.Context(), mustJSON(t, map[string]any{"path": "f.txt"}))
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if !strings.Contains(got, "3: three") {
		t.Errorf("no args should read whole file, got %q", got)
	}
	got, err = tool.Execute(t.Context(), mustJSON(t, map[string]any{"path": "f.txt", "limit": 2}))
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if got != "1: one\n2: two\n" {
		t.Errorf("limit alone should read first lines, got %q", got)
	}
}

func TestRead_NegativeLimitErrors(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	writeFile(t, root, "f.txt", "a\nb\n")
	tool := tools.ReadTool{Root: root}
	_, err := tool.Execute(t.Context(), mustJSON(t, map[string]any{
		"path": "f.txt", "limit": -5,
	}))
	if err == nil || !strings.Contains(err.Error(), "limit") {
		t.Errorf("negative limit should error, got: %v", err)
	}
}

func TestRead_RejectsBinaryFile(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	writeFile(t, root, "blob.bin", "\x7fELF\x02\x01\x01\x00"+strings.Repeat("\x00", 64))
	tool := tools.ReadTool{Root: root}
	got, err := tool.Execute(t.Context(), mustJSON(t, map[string]string{"path": "blob.bin"}))
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if !strings.Contains(got, "binary") {
		t.Errorf("binary file should be reported, got %q", got)
	}
}
