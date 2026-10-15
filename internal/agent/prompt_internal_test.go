package agent

import (
	"runtime"
	"strings"
	"testing"
	"time"
)

// The built-in prompt is the contract for ADR-0031: a minimal baseline whose
// safety clauses (user-work protection, untrusted data, secrets) exist
// because tool execution is ungated (ADR-0016). Exported-constant tests
// follow the TitleSystemPrompt pattern (internal/server/title.go).

func TestDefaultSystemPrompt_ContainsBaselineClauses(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name string
		mark string
		why  string
	}{
		{
			name: "identity",
			mark: "You are golem",
			why:  "role line anchors the agent identity",
		},
		{
			name: "verify before claiming done",
			mark: "Verify your changes",
			why:  "prevents unverified completion claims",
		},
		{
			name: "user work protection",
			mark: "you did not make",
			why:  "autonomous execution must not discard user edits (ADR-0016)",
		},
		{
			name: "no destructive commands",
			mark: "git reset --hard",
			why:  "named dangerous commands stay forbidden without a gate",
		},
		{
			name: "no unrequested commits",
			mark: "commit",
			why:  "git history is user-owned",
		},
		{
			name: "untrusted tool output",
			mark: "not instructions",
			why:  "webfetch content must not steer the agent (ADR-0021)",
		},
		{
			name: "secrets",
			mark: "secrets",
			why:  "credential hygiene in transcripts and commands",
		},
		{
			name: "persistence",
			mark: "retry",
			why:  "the autonomous loop must not give up on first failure",
		},
		{
			name: "parallel tool calls",
			mark: "parallel",
			why:  "independent calls batch in one round",
		},
		{
			name: "answer vs act",
			mark: "answer it",
			why:  "questions must not trigger edits",
		},
		{
			name: "mirror user language",
			mark: "language",
			why:  "responses follow the user's writing language",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if !strings.Contains(defaultSystemPrompt, tc.mark) {
				t.Errorf("default prompt missing %q (%s):\n%s", tc.mark, tc.why,
					defaultSystemPrompt)
			}
		})
	}
}

func TestDefaultSystemPrompt_NoAGENTSStringWithoutFiles(t *testing.T) {
	t.Parallel()
	// Mirrors TestServer_TurnWorksWithoutAGENTSFiles: with no discovered
	// files the assembled prompt must not name AGENTS.md — the ordering
	// clause only appears when files exist.
	if strings.Contains(defaultSystemPrompt, "AGENTS.md") {
		t.Errorf("baseline prompt must not mention AGENTS.md, got:\n%s",
			defaultSystemPrompt)
	}
}

func TestEnvironmentContext(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name string
		cwd  string
		now  time.Time
		must []string
	}{
		{
			name: "unix path",
			cwd:  "/home/u/proj",
			now:  time.Date(2026, 10, 16, 12, 0, 0, 0, time.UTC),
			must: []string{"/home/u/proj", "2026-10-16"},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got := EnvironmentContext(tc.cwd, tc.now)
			for _, mark := range tc.must {
				if !strings.Contains(got, mark) {
					t.Errorf("EnvironmentContext missing %q, got:\n%s", mark, got)
				}
			}
			if !strings.Contains(got, runtime.GOOS) {
				t.Errorf("EnvironmentContext missing platform %q, got:\n%s",
					runtime.GOOS, got)
			}
		})
	}
}

func TestEnvironmentContext_EmptyCwdOmitsLine(t *testing.T) {
	t.Parallel()
	got := EnvironmentContext("", time.Date(2026, 10, 16, 0, 0, 0, 0, time.UTC))
	if strings.Contains(got, "Working directory") {
		t.Errorf("empty cwd must omit the directory line, got:\n%s", got)
	}
}
