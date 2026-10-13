package cli_test

import (
	"io"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/dev-hann/golem/internal/cli"
)

// lockedBuffer is a mutex-guarded buffer: the wizard goroutine writes
// while the test polls.
type lockedBuffer struct {
	mu  sync.Mutex
	buf strings.Builder
}

func (b *lockedBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()

	return b.buf.Write(p)
}

func (b *lockedBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()

	return b.buf.String()
}

// TestRunOnboard_PromptAppearsBeforeInput guards against the interactive
// hang where prompts sit in an output buffer while the wizard blocks on
// stdin: the provider prompt must be visible before any input arrives.
func TestRunOnboard_PromptAppearsBeforeInput(t *testing.T) {
	t.Parallel()

	stdinReader, stdinWriter := io.Pipe() // no input until the test provides it
	t.Cleanup(func() { _ = stdinWriter.Close() })

	out := &lockedBuffer{}

	done := make(chan error, 1)
	go func() {
		done <- cli.RunOnboard(cli.OnboardOptions{
			ConfigPath: filepath.Join(t.TempDir(), "config.toml"),
			Stdin:      stdinReader,
			Stdout:     out,
			Stderr:     io.Discard,
			TestKey:    func(_, _, _ string) error { return nil },
		})
	}()

	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if strings.Contains(out.String(), "choose a provider") {
			_ = stdinWriter.Close() // EOF ends the wizard; the prompt already showed

			select {
			case <-done:
			case <-time.After(2 * time.Second):
				t.Fatal("wizard did not return after stdin closed")
			}

			return
		}

		time.Sleep(10 * time.Millisecond)
	}

	t.Fatal("provider prompt never appeared before input — output is stuck in a buffer")
}
