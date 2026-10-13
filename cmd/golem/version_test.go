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

func TestResolveVersion_LdflagsWin(t *testing.T) {
	t.Parallel()

	origVersion, origCommit := version, commit
	t.Cleanup(func() { version, commit = origVersion, origCommit })

	version, commit = "v9.9.9", "abc1234"
	if got := resolveVersion(); got != "v9.9.9" {
		t.Errorf("resolveVersion() = %q, want ldflags value", got)
	}
}

func TestResolveVersion_DevFallback(t *testing.T) {
	t.Parallel()

	// Test binaries have no ldflags injection and a (devel) build info
	// version, so the dev default must survive.
	if got := resolveVersion(); got == "" {
		t.Error("resolveVersion() = empty")
	}
}
