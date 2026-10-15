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

// ADR-0022: `golem chat` /compact sends compact_session and reports the
// barrier; compacting an empty (never-spoken) session reports the
// refusal without crashing.

func TestChat_CompactCommand(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name     string
		stdin    string
		wantOut  string
		wantNone string
	}{
		{
			name:    "compact after a message reports the barrier",
			stdin:   "hello\n/compact\n/quit\n",
			wantOut: "context compacted",
		},
		{
			name:     "compact before any message is refused",
			stdin:    "/compact\n/quit\n",
			wantNone: "context compacted",
		},
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

			var out strings.Builder
			if err := cli.Chat(t.Context(), cli.ChatOptions{
				Socket: socket, Stdin: strings.NewReader(tc.stdin),
				Stdout: io.Discard, Stderr: &out,
			}); err != nil {
				t.Fatalf("Chat: %v", err)
			}

			got := out.String()
			if tc.wantOut != "" && !strings.Contains(got, tc.wantOut) {
				t.Errorf("stderr missing %q, got:\n%s", tc.wantOut, got)
			}
			if tc.wantNone != "" && strings.Contains(got, tc.wantNone) {
				t.Errorf("stderr must not contain %q, got:\n%s", tc.wantNone, got)
			}
		})
	}
}
