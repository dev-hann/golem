package agent_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/dev-hann/golem/internal/agent"
	"github.com/dev-hann/golem/internal/llm"
	"github.com/dev-hann/golem/internal/tools"
)

// setupInstructionsDirs builds an isolated global config dir (wired via
// XDG_CONFIG_HOME) and a project cwd; the caller writes files into them.
// Callers must not run in parallel: t.Setenv forbids it.
func setupInstructionsDirs(t *testing.T) (cfgDir, project string) {
	t.Helper()

	cfgDir = t.TempDir()
	project = t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", cfgDir)

	return cfgDir, project
}

// writeInstructions writes content to path, creating parents as needed.
func writeInstructions(t *testing.T, path, content string) {
	t.Helper()

	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("mkdir %s: %v", path, err)
	}

	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}

func TestDiscoverInstructions(t *testing.T) {
	// Sequential: subtests mutate XDG_CONFIG_HOME via t.Setenv.
	cases := []struct {
		name       string
		global     string
		project    string
		want       string
		wantAbsent string
	}{
		{
			name:       "global only",
			global:     "GLOBAL RULES",
			want:       "GLOBAL RULES",
			wantAbsent: "PROJECT",
		},
		{
			name:       "project only",
			project:    "PROJECT RULES",
			want:       "PROJECT RULES",
			wantAbsent: "GLOBAL",
		},
		{
			name:    "both files are appended, global first",
			global:  "GLOBAL RULES",
			project: "PROJECT RULES",
			want:    "GLOBAL RULES",
		},
		{
			name: "no files yields empty",
		},
		{
			name:    "whitespace-only files are ignored",
			global:  " \n\t",
			project: "\n",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cfgDir, project := setupInstructionsDirs(t)
			if tc.global != "" || tc.name == "whitespace-only files are ignored" {
				writeInstructions(t, filepath.Join(cfgDir, "golem", "AGENTS.md"), tc.global)
			}
			if tc.project != "" || tc.name == "whitespace-only files are ignored" {
				writeInstructions(t, filepath.Join(project, "AGENTS.md"), tc.project)
			}

			got := agent.DiscoverInstructions(project)
			switch {
			case tc.want == "":
				if got != "" {
					t.Errorf("DiscoverInstructions = %q, want empty", got)
				}
			case !strings.Contains(got, tc.want):
				t.Errorf("DiscoverInstructions missing %q, got:\n%s", tc.want, got)
			}

			if tc.wantAbsent != "" && strings.Contains(got, tc.wantAbsent) {
				t.Errorf("DiscoverInstructions must not contain %q, got:\n%s", tc.wantAbsent, got)
			}
		})
	}
}

func TestDiscoverInstructions_GlobalBeforeProject(t *testing.T) {
	// Sequential: mutates XDG_CONFIG_HOME via t.Setenv.
	cfgDir, project := setupInstructionsDirs(t)
	writeInstructions(t, filepath.Join(cfgDir, "golem", "AGENTS.md"), "GLOBAL-MARKER")
	writeInstructions(t, filepath.Join(project, "AGENTS.md"), "PROJECT-MARKER")

	got := agent.DiscoverInstructions(project)
	globalAt := strings.Index(got, "GLOBAL-MARKER")
	projectAt := strings.Index(got, "PROJECT-MARKER")
	if globalAt < 0 || projectAt < 0 {
		t.Fatalf("missing markers in:\n%s", got)
	}
	if globalAt > projectAt {
		t.Errorf("global instructions must precede project ones (global@%d project@%d):\n%s",
			globalAt, projectAt, got)
	}
}

func TestDiscoverInstructions_ClipsOversizedFile(t *testing.T) {
	// Sequential: mutates XDG_CONFIG_HOME via t.Setenv.
	_, project := setupInstructionsDirs(t)
	huge := strings.Repeat("x", agent.InstructionsMaxBytes+4096)
	writeInstructions(t, filepath.Join(project, "AGENTS.md"), huge)

	got := agent.DiscoverInstructions(project)
	if !strings.Contains(got, "[truncated]") {
		t.Errorf("oversized file must carry a truncation marker")
	}

	// Header + clip + marker must stay near the cap, not the raw size.
	if len(got) > agent.InstructionsMaxBytes+512 {
		t.Errorf("result is %d bytes, want it clipped near %d", len(got), agent.InstructionsMaxBytes)
	}
}

func TestLoop_InstructionsAppendAfterSystemPrompt(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name string
		opts []agent.Option
		want string
	}{
		{
			name: "extra appends after the custom prompt",
			opts: []agent.Option{
				agent.WithSystemPrompt("base prompt"),
				agent.WithInstructions("AGENTS-MARKER"),
			},
			want: "base prompt\n\nAGENTS-MARKER",
		},
		{
			name: "empty extra is a no-op",
			opts: []agent.Option{
				agent.WithSystemPrompt("base prompt"),
				agent.WithInstructions(""),
			},
			want: "base prompt",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			provider := &scriptedProvider{rounds: [][]llm.Event{
				{{Type: llm.EventTextDelta, Delta: "ok"}},
			}}
			_, root := newTestAgent(t, provider)
			a := agent.New(provider, mustRegistry(t, root), "m", tc.opts...)
			collect(t, a.Run(t.Context(), nil, "hi"))
			if got := provider.requests[0].SystemPrompt; got != tc.want {
				t.Errorf("system prompt = %q, want %q", got, tc.want)
			}
		})
	}
}

// compile-time guard: the loop package stays importable from this test.
var _ = tools.DefaultOutputBudget
