package cli_test

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/dev-hann/golem/internal/cli"
	"github.com/dev-hann/golem/internal/session"
)

// shutdownLatency bounds how fast Serve must return after the shutdown hook
// fires. The bug this guards against: Serve blocked forever in Accept
// because nothing closed the listener.
const shutdownLatency = 5 * time.Second

func TestServe_ReturnsAfterShutdownSignal(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	socket := filepath.Join(dir, "golem.sock")
	store, err := session.NewStore(filepath.Join(dir, "sessions"), 2)
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}

	ready := make(chan struct{})
	done := make(chan error, 1)

	go func() {
		done <- cli.Serve(context.Background(), cli.ServeOptions{
			Provider: echoProvider{}, Model: "stub", Root: dir,
			Socket: socket, Store: store,
			OnReady: func(string) { close(ready) },
			SignalCtx: func(_ context.Context, shutdown context.CancelFunc) {
				go func() {
					<-ready
					shutdown()
				}()
			},
		})
	}()

	select {
	case <-ready:
	case <-time.After(shutdownLatency):
		t.Fatal("server did not become ready")
	}

	select {
	case err := <-done:
		if err != nil {
			t.Errorf("Serve returned error after shutdown: %v", err)
		}
	case <-time.After(shutdownLatency):
		t.Fatal("Serve did not return after shutdown signal")
	}
}
