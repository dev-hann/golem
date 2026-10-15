package cli_test

import (
	"bytes"
	"context"
	"encoding/json"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/dev-hann/golem/internal/cli"
	"github.com/dev-hann/golem/internal/llm"
	"github.com/dev-hann/golem/internal/server"
	"github.com/dev-hann/golem/internal/session"
)

// bashProvider requests a bash call once, then finishes with text. Title
// side calls (ADR-0032 fires them at turn start) get a canned stream
// without consuming the scripted round, and calls is counted under a
// mutex because the side call races the agent loop.
type bashProvider struct {
	mu    sync.Mutex
	calls int
}

func (p *bashProvider) Stream(
	_ context.Context,
	req llm.Request,
) (<-chan llm.Event, error) {
	if req.SystemPrompt == server.TitleSystemPrompt {
		out := make(chan llm.Event, 1)
		out <- llm.Event{Type: llm.EventTextDelta, Delta: "Test title"}
		close(out)

		return out, nil
	}

	p.mu.Lock()
	p.calls++
	calls := p.calls
	p.mu.Unlock()

	out := make(chan llm.Event, 2)
	if calls == 1 {
		out <- llm.Event{
			Type: llm.EventToolCalls,
			ToolCalls: []llm.ToolCall{{
				ID: "c1", Name: "bash", Args: json.RawMessage(`{"command":"echo golem-chat-ran"}`),
			}},
		}
	} else {
		out <- llm.Event{Type: llm.EventTextDelta, Delta: "ran and done"}
	}
	close(out)
	return out, nil
}

func TestChat_BashRunsAutonomouslyAndShowsInTranscript(t *testing.T) {
	t.Parallel()
	sockDir := shortTempDir(t)
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
	if !strings.Contains(errOut, "→ bash") {
		t.Errorf("stderr should show the tool call: %q", errOut)
	}
	if !strings.Contains(errOut, "✓") {
		t.Errorf("stderr should show the tool succeeded: %q", errOut)
	}
	if !strings.Contains(stdout.String(), "ran and done") {
		t.Errorf("stdout missing final answer: %q", stdout.String())
	}
}
