package server

import (
	"strings"
	"testing"
	"time"

	"github.com/dev-hann/golem/internal/llm"
)

func TestSanitizeTitle(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name string
		raw  string
		want string
	}{
		{"plain", "Fix auth middleware", "Fix auth middleware"},
		{"first line only", "Fix auth\nexplanation follows", "Fix auth"},
		{"quoted", `"Quoted title"`, "Quoted title"},
		{"single quoted", "'Single'", "Single"},
		{"padded", "  Padded  ", "Padded"},
		{"empty", "   ", ""},
		{"newline only", "\n", ""},
		{
			"over cap keeps head and ellipsis",
			strings.Repeat("x", 100),
			strings.Repeat("x", titleMaxRunes) + "…",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			if got := sanitizeTitle(tc.raw); got != tc.want {
				t.Errorf("sanitizeTitle(%q) = %q, want %q", tc.raw, got, tc.want)
			}
		})
	}
}

// TestTitleTimeoutCoversRetryLadder guards the side call's deadline: the
// provider-level retry ladder (llm.Retrying) waits the full backoff sum on
// worst-case transient failures (~61s across six retries). A shorter
// title timeout would cut the ladder mid-wait, so a rate-limited provider
// that would have recovered never gets the chance.
func TestTitleTimeoutCoversRetryLadder(t *testing.T) {
	t.Parallel()

	policy := llm.DefaultRetryPolicy()

	var ladder time.Duration
	for i := 1; i <= policy.Retries; i++ {
		ladder += min(policy.Backoff<<uint(i-1), policy.MaxBackoff)
	}

	// The deadline must cover every backoff plus per-attempt time.
	if titleTimeout < ladder {
		t.Errorf("titleTimeout = %v, want at least the retry ladder %v", titleTimeout, ladder)
	}
}
