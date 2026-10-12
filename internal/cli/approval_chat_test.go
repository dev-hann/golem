package cli_test

import (
	"bytes"
	"context"
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/golem-agent/golem/internal/cli"
	"github.com/golem-agent/golem/internal/llm"
	"github.com/golem-agent/golem/internal/session"
)

// bashProvider requests a bash call once, then finishes with text.
type bashProvider struct {
	calls int
}

func (p *bashProvider) Stream(
	_ context.Context,
	_ llm.Request,
) (<-chan llm.Event, error) {
	p.calls++
	out := make(chan llm.Event, 2)
	if p.calls == 1 {
		out <- llm.Event{
			Type: llm.EventToolCalls,
			ToolCalls: []llm.ToolCall{{
				ID: "c1", Name: "bash", Args: json.RawMessage(`{"command":"echo never-runs"}`),
			}},
		}
	} else {
		out <- llm.Event{Type: llm.EventTextDelta, Delta: "denied and done"}
	}
	close(out)
	return out, nil
}

func TestChat_AutoDeniesApprovalRequests(t *testing.T) {
	t.Parallel()
	sockDir := t.TempDir()
	socket := filepath.Join(sockDir, "golem.sock")
	store, err := session.NewStore(filepath.Join(sockDir, "sessions"), 4)
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	ready := make(chan struct{})
	go func() {
		_ = cli.Serve(context.Background(), cli.ServeOptions{
			Provider: &bashProvider{}, Model: "stub", Root: t.TempDir(),
			Socket: socket, Store: store, OnReady: func(string) { close(ready) },
		})
	}()
	select {
	case <-ready:
	case <-time.After(5 * time.Second):
		t.Fatal("server not ready")
	}
	stdin := strings.NewReader("run something\n/quit\n")
	var stdout, stderr bytes.Buffer
	err = cli.Chat(t.Context(), cli.ChatOptions{
		Socket: socket, Stdin: stdin, Stdout: &stdout, Stderr: &stderr,
	})
	if err != nil {
		t.Fatalf("Chat: %v", err)
	}
	errOut := stderr.String()
	if !strings.Contains(errOut, "auto-denied") {
		t.Errorf("stderr should note auto-denial: %q", errOut)
	}
	if !strings.Contains(errOut, "✗") {
		t.Errorf("stderr should show the denied tool call: %q", errOut)
	}
	if strings.Contains(errOut, "max tool rounds") {
		t.Errorf("provider should move on after denial: %q", errOut)
	}
	if !strings.Contains(stdout.String(), "denied and done") {
		t.Errorf("stdout missing final answer: %q", stdout.String())
	}
}
