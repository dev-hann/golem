package cli_test

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/dev-hann/golem/internal/cli"
	"github.com/dev-hann/golem/internal/llm"
	"github.com/dev-hann/golem/internal/session"
)

// echoProvider answers every stream with one text delta.
type echoProvider struct{}

func (echoProvider) Stream(
	_ context.Context,
	_ llm.Request,
) (<-chan llm.Event, error) {
	out := make(chan llm.Event, 1)
	out <- llm.Event{Type: llm.EventTextDelta, Delta: "echoed"}
	close(out)
	return out, nil
}

func TestServe_AndChatSmoke(t *testing.T) {
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
			Provider: echoProvider{},
			Model:    "stub",
			Root:     t.TempDir(),
			Socket:   socket,
			Store:    store,
			OnReady:  func(string) { close(ready) },
		})
	}()
	select {
	case <-ready:
	case <-time.After(5 * time.Second):
		t.Fatal("server did not become ready")
	}
	stdin := strings.NewReader("hello\n/quit\n")
	var stdout, stderr bytes.Buffer
	err = cli.Chat(t.Context(), cli.ChatOptions{
		Socket: socket, Stdin: stdin, Stdout: &stdout, Stderr: &stderr,
	})
	if err != nil {
		t.Fatalf("Chat: %v", err)
	}
	out := stdout.String()
	if !strings.Contains(out, "session ") {
		t.Errorf("stdout missing session banner: %q", out)
	}
	if !strings.Contains(out, "echoed") {
		t.Errorf("stdout missing answer: %q", out)
	}
	if !strings.Contains(stderr.String(), "tokens:") {
		t.Errorf("stderr missing usage line: %q", stderr.String())
	}
}

func TestServe_SocketErrorsBubble(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	blocked, err := os.Create(filepath.Join(dir, "occupied"))
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	defer blocked.Close()
	err = cli.Serve(t.Context(), cli.ServeOptions{
		Provider: echoProvider{}, Model: "stub", Root: dir,
		Socket: filepath.Join(dir, "occupied"), Sessions: filepath.Join(dir, "s"),
	})
	if err == nil || !strings.Contains(err.Error(), "listening") {
		t.Errorf("expected listen error, got: %v", err)
	}
}

func TestChat_UnreachableSocketErrors(t *testing.T) {
	t.Parallel()
	err := cli.Chat(t.Context(), cli.ChatOptions{
		Socket: filepath.Join(t.TempDir(), "missing.sock"),
		Stdin:  strings.NewReader(""),
		Stdout: &bytes.Buffer{},
		Stderr: &bytes.Buffer{},
	})
	if err == nil || !strings.Contains(err.Error(), "dial") {
		t.Errorf("expected dial error, got: %v", err)
	}
}

func TestDefaultSocketPath_RespectsXDGRuntime(t *testing.T) {
	t.Setenv("XDG_RUNTIME_DIR", "/tmp/run-test")
	if got := cli.DefaultSocketPath(); got != "/tmp/run-test/golem.sock" {
		t.Errorf("DefaultSocketPath() = %q", got)
	}
	t.Setenv("XDG_RUNTIME_DIR", "")
	t.Setenv("XDG_DATA_HOME", "/tmp/data-test")
	if got := cli.DefaultSocketPath(); got != "/tmp/data-test/golem/golem.sock" {
		t.Errorf("fallback DefaultSocketPath() = %q", got)
	}
	if got := cli.DefaultSessionsDir(); got != "/tmp/data-test/golem/sessions" {
		t.Errorf("DefaultSessionsDir() = %q", got)
	}
}
