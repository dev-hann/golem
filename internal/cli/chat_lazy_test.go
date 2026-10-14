package cli_test

import (
	"io"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/dev-hann/golem/internal/cli"
	"github.com/dev-hann/golem/internal/session"
)

// ADR-0018: chat creates its session lazily — quitting before the first
// message must leave the store empty.

func TestChat_NoSessionUntilFirstMessage(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name    string
		stdin   string
		wantOne bool
	}{
		{name: "immediate quit creates nothing", stdin: "/quit\n", wantOne: false},
		{name: "blank lines create nothing", stdin: "\n  \n/exit\n", wantOne: false},
		{name: "first message creates the session", stdin: "hello\n/quit\n", wantOne: true},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			sockDir := shortTempDir(t)
			socket := filepath.Join(sockDir, "golem.sock")
			store, err := session.NewStore(filepath.Join(sockDir, "sessions"), 4)
			if err != nil {
				t.Fatalf("NewStore: %v", err)
			}

			ready := make(chan struct{})
			go func() {
				_ = cli.Serve(t.Context(), cli.ServeOptions{
					Provider: echoProvider{}, Model: "stub", Root: t.TempDir(),
					Socket: socket, Store: store, OnReady: func(string) { close(ready) },
				})
			}()
			select {
			case <-ready:
			case <-time.After(5 * time.Second):
				t.Fatal("server not ready")
			}

			if err := cli.Chat(t.Context(), cli.ChatOptions{
				Socket: socket, Stdin: strings.NewReader(tc.stdin), Stdout: io.Discard, Stderr: io.Discard,
			}); err != nil {
				t.Fatalf("Chat: %v", err)
			}

			summaries, err := store.List()
			if err != nil {
				t.Fatalf("List: %v", err)
			}

			created := len(summaries) > 0
			if created != tc.wantOne {
				t.Errorf("sessions created = %v, want %v (store: %+v)", created, tc.wantOne, summaries)
			}
		})
	}
}
