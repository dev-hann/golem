package tools_test

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/golem-agent/golem/internal/tools"
)

// stubTool is a minimal Tool for registry tests.
type stubTool struct {
	name string
}

func (s stubTool) Name() string              { return s.name }
func (stubTool) Description() string         { return "stub" }
func (stubTool) InputSchema() map[string]any { return map[string]any{} }
func (stubTool) Execute(context.Context, json.RawMessage) (string, error) {
	return "stub", nil
}

func TestRegistry_RejectsDuplicateNames(t *testing.T) {
	t.Parallel()
	_, err := tools.NewRegistry(stubTool{name: "dup"}, stubTool{name: "dup"})
	if err == nil {
		t.Fatal("expected duplicate-name error, got nil")
	}
	if !strings.Contains(err.Error(), "duplicate") {
		t.Errorf("error should mention duplicate, got: %v", err)
	}
}

func TestRegistry_RejectsEmptyName(t *testing.T) {
	t.Parallel()
	_, err := tools.NewRegistry(stubTool{name: ""})
	if err == nil {
		t.Fatal("expected empty-name error, got nil")
	}
}

func TestRegistry_GetFindsRegisteredTool(t *testing.T) {
	t.Parallel()
	reg, err := tools.NewRegistry(stubTool{name: "alpha"}, stubTool{name: "beta"})
	if err != nil {
		t.Fatalf("NewRegistry: %v", err)
	}
	if _, ok := reg.Get("alpha"); !ok {
		t.Error("alpha should be registered")
	}
	if _, ok := reg.Get("missing"); ok {
		t.Error("missing should not be registered")
	}
}

func TestRegistry_SpecsSortedByName(t *testing.T) {
	t.Parallel()
	reg, err := tools.NewRegistry(stubTool{name: "zeta"}, stubTool{name: "alpha"})
	if err != nil {
		t.Fatalf("NewRegistry: %v", err)
	}
	specs := reg.Specs()
	if len(specs) != 2 || specs[0].Name != "alpha" || specs[1].Name != "zeta" {
		t.Errorf("specs not sorted: %v", specs)
	}
}

func TestRegistry_ExecuteUnknownToolErrors(t *testing.T) {
	t.Parallel()
	reg, err := tools.NewRegistry(stubTool{name: "alpha"})
	if err != nil {
		t.Fatalf("NewRegistry: %v", err)
	}
	_, err = reg.Execute(t.Context(), "nope", json.RawMessage("{}"))
	if err == nil || !strings.Contains(err.Error(), "unknown tool") {
		t.Errorf("expected unknown-tool error, got: %v", err)
	}
}

func TestTruncate(t *testing.T) {
	t.Parallel()
	markerLen := len("\n...[truncated]")
	tests := []struct {
		name      string
		input     string
		budget    int
		wantCut   bool
		wantInRes string
	}{
		{name: "under budget untouched", input: "short", budget: 100, wantCut: false},
		{name: "exact budget untouched", input: "12345", budget: 5, wantCut: false},
		{
			name: "over budget cut and marked", input: strings.Repeat("a", 300), budget: 100,
			wantCut: true, wantInRes: "[truncated]",
		},
		{
			name: "utf8 boundary respected", input: strings.Repeat("한", 100), budget: 30,
			wantCut: true, wantInRes: "[truncated]",
		},
		{name: "non positive budget is no-op", input: "keep", budget: 0, wantCut: false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got, cut := tools.Truncate(tt.input, tt.budget)
			if cut != tt.wantCut {
				t.Errorf("Truncate(%d) cut = %v, want %v", tt.budget, cut, tt.wantCut)
			}
			if tt.wantCut {
				maxLen := tt.budget + markerLen
				if len(got) > maxLen {
					t.Errorf("result %d bytes exceeds budget+marker %d", len(got), maxLen)
				}
				if !strings.Contains(got, tt.wantInRes) {
					t.Errorf("result missing marker: %q", got)
				}
				if !strings.HasPrefix(got, tt.input[:len(got)-markerLen]) {
					t.Error("result should be a prefix of input plus marker")
				}
			} else if got != tt.input {
				t.Errorf("uncut result changed: %q", got)
			}
		})
	}
}

func TestReadBlocksPathEscape(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	tool := tools.ReadTool{Root: root}
	tests := []struct {
		name string
		path string
	}{
		{name: "parent traversal", path: "../../etc/passwd"},
		{name: "absolute outside root", path: "/etc/passwd"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			_, err := tool.Execute(t.Context(), mustJSON(t, map[string]string{"path": tt.path}))
			if err == nil {
				t.Fatalf("expected escape error for %q, got nil", tt.path)
			}
			if !strings.Contains(err.Error(), "escapes") {
				t.Errorf("error should mention escape, got: %v", err)
			}
		})
	}
}

// mustJSON marshals v for tool args.
func mustJSON(t *testing.T, v any) json.RawMessage {
	t.Helper()
	data, err := json.Marshal(v)
	if err != nil {
		t.Fatalf("marshal args: %v", err)
	}
	return data
}
