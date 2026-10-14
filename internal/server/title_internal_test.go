package server

import (
	"strings"
	"testing"
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
