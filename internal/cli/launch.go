// Launcher for the default `golem` command: make sure a server is running,
// then start the TUI against it.

package cli

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"syscall"
	"time"
)

// Launcher timings: dial probe budget, readiness re-check interval after a
// spawn, and the overall startup budget before giving up.
const (
	probeTimeout = 500 * time.Millisecond
	pollInterval = 100 * time.Millisecond
	readyTimeout = 5 * time.Second
)

// AppOptions configures the default app launch (bare `golem` / `golem tui`).
type AppOptions struct {
	Socket string
	// Start spawns a server on Socket when none is running. Production
	// wiring spawns the golem executable detached (see SpawnServe).
	Start func() error
	// Stderr receives progress messages such as "starting server".
	Stderr io.Writer
}

// Probe reports whether a golem server is accepting connections on socket.
func Probe(socket string) bool {
	dialer := net.Dialer{Timeout: probeTimeout}

	conn, err := dialer.DialContext(context.Background(), "unix", socket)
	if err != nil {
		return false
	}

	_ = conn.Close()

	return true
}

// EnsureServer guarantees a server is reachable on socket, starting it via
// start when it is not. A socket file left behind by a crashed server is
// removed so the fresh listener can bind.
func EnsureServer(ctx context.Context, socket string, start func() error, stderr io.Writer) error {
	if Probe(socket) {
		return nil
	}

	if err := removeStaleSocket(socket); err != nil {
		return err
	}

	fmt.Fprintf(stderr, "golem: starting server on %s\n", socket) //nolint:errcheck // terminal output

	if err := start(); err != nil {
		return fmt.Errorf("cli: starting server: %w", err)
	}

	deadline := time.Now().Add(readyTimeout)

	for !Probe(socket) {
		if err := ctx.Err(); err != nil {
			return fmt.Errorf("cli: server on %s not ready: %w", socket, err)
		}

		if time.Now().After(deadline) {
			return fmt.Errorf("cli: server on %s not ready within %s", socket, readyTimeout)
		}

		timer := time.NewTimer(pollInterval)
		select {
		case <-ctx.Done():
			timer.Stop()

			return fmt.Errorf("cli: server on %s not ready: %w", socket, ctx.Err())
		case <-timer.C:
		}
	}

	return nil
}

// removeStaleSocket deletes socket when the file exists but nothing listens
// on it; a leftover file would make the next bind fail with EADDRINUSE.
func removeStaleSocket(socket string) error {
	if _, err := os.Stat(socket); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}

		return fmt.Errorf("cli: checking socket %s: %w", socket, err)
	}

	if err := os.Remove(socket); err != nil {
		return fmt.Errorf("cli: removing stale socket %s: %w", socket, err)
	}

	return nil
}

// SpawnServe starts exe as a detached `golem serve` process. Production
// callers pass os.Executable()'s result so golem re-executes itself; the
// server outlives the client that spawned it.
func SpawnServe(exe string) error {
	if exe == "" {
		return fmt.Errorf("cli: spawn: no executable path")
	}

	// Detached daemon: intentionally no cancellation tied to the client.
	cmd := exec.CommandContext(context.Background(), exe, "serve")
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true} // detach from the client's session

	if err := cmd.Start(); err != nil {
		return fmt.Errorf("cli: spawning %s: %w", exe, err)
	}

	return nil
}

// RunApp is the body of bare `golem`: ensure the server is up (spawning it
// when needed), then run the TUI against it.
func RunApp(ctx context.Context, opts AppOptions) error {
	if err := EnsureServer(ctx, opts.Socket, opts.Start, opts.Stderr); err != nil {
		return err
	}

	return RunTUI(opts.Socket)
}
