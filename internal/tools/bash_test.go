package tools_test

import (
	"context"
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/dev-hann/golem/internal/tools"
)

func TestBash_CapturesStdoutAndStderr(t *testing.T) {
	t.Parallel()
	tool := tools.BashTool{Root: t.TempDir()}
	got, err := tool.Execute(t.Context(), mustJSON(t,
		map[string]string{"command": "echo out; echo err 1>&2"}))
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if !strings.Contains(got, "out") || !strings.Contains(got, "err") {
		t.Errorf("combined output missing streams: %q", got)
	}
}

func TestBash_RespectsTimeout(t *testing.T) {
	t.Parallel()
	tool := tools.BashTool{Root: t.TempDir()}
	start := time.Now()
	_, err := tool.Execute(t.Context(), mustJSON(t,
		map[string]any{"command": "sleep 30", "timeout_secs": 1}))
	if err == nil {
		t.Fatal("expected timeout error, got nil")
	}
	if elapsed := time.Since(start); elapsed > 10*time.Second {
		t.Errorf("timeout took %v, want ~1s", elapsed)
	}
	if !strings.Contains(err.Error(), "context deadline exceeded") {
		t.Errorf("error should be deadline, got: %v", err)
	}
}

func TestBash_KillsProcessGroupOnCancel(t *testing.T) {
	tool := tools.BashTool{Root: t.TempDir()}
	ctx, cancel := context.WithCancel(t.Context())
	go func() {
		time.Sleep(300 * time.Millisecond)
		cancel()
	}()
	_, err := tool.Execute(ctx, mustJSON(t,
		map[string]string{"command": "sleep 91.731 & sleep 91.731; wait"}))
	if err == nil {
		t.Fatal("expected cancel error, got nil")
	}
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		out, _ := exec.CommandContext(t.Context(), "pgrep", "-f", "sleep 91.731").Output()
		if len(out) == 0 {
			return // process group gone — success
		}
		time.Sleep(200 * time.Millisecond)
	}
	t.Error("sleep children still alive after cancel; process group not killed")
}

func TestBash_ErrorsOnMissingCommand(t *testing.T) {
	t.Parallel()
	tool := tools.BashTool{Root: t.TempDir()}
	_, err := tool.Execute(t.Context(), mustJSON(t, map[string]string{}))
	if err == nil || !strings.Contains(err.Error(), "missing command") {
		t.Errorf("expected missing-command error, got: %v", err)
	}
}
