package main

import (
	"strings"
	"testing"
)

func TestRunVersion_PrintsBuildInfo(t *testing.T) {
	t.Parallel()

	var out strings.Builder

	if got := runVersion(&out); got != 0 {
		t.Errorf("runVersion() = %d, want 0", got)
	}

	got := out.String()
	if !strings.HasPrefix(got, "golem ") {
		t.Errorf("version output = %q, want golem prefix", got)
	}

	if !strings.Contains(got, "commit") {
		t.Errorf("version output = %q, want commit mention", got)
	}
}

func TestResolveVersionFrom(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		in   string
		want string
	}{
		// Test binaries carry no ldflags and a (devel) build info version,
		// so the dev default must survive.
		{name: "dev falls through", in: "dev", want: "dev"},
		{name: "ldflags value wins", in: "v9.9.9", want: "v9.9.9"},
		{name: "git describe wins", in: "v0.1.0-3-gabc", want: "v0.1.0-3-gabc"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			if got := resolveVersionFrom(tt.in); got != tt.want {
				t.Errorf("resolveVersionFrom(%q) = %q, want %q", tt.in, got, tt.want)
			}
		})
	}
}
