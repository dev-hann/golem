package cli_test

import (
	"context"
	"errors"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/dev-hann/golem/internal/cli"
)

// listenUnix opens a listening unix socket so Probe-style dials succeed.
func listenUnix(t *testing.T, socket string) {
	t.Helper()

	l, err := (&net.ListenConfig{}).Listen(t.Context(), "unix", socket)
	if err != nil {
		t.Fatalf("listen %s: %v", socket, err)
	}
	t.Cleanup(func() { _ = l.Close() })
}

func TestProbe(t *testing.T) {
	t.Parallel()

	socket := filepath.Join(t.TempDir(), "golem.sock")

	if cli.Probe(socket) {
		t.Error("Probe(missing socket) = true, want false")
	}

	stale, err := os.Create(socket)
	if err != nil {
		t.Fatalf("create stale file: %v", err)
	}
	stale.Close()
	if cli.Probe(socket) {
		t.Error("Probe(stale plain file) = true, want false")
	}

	if err := os.Remove(socket); err != nil {
		t.Fatalf("remove stale file: %v", err)
	}

	listenUnix(t, socket)
	if !cli.Probe(socket) {
		t.Error("Probe(listening socket) = false, want true")
	}
}

func TestEnsureServer_AlreadyRunningSkipsStart(t *testing.T) {
	t.Parallel()

	socket := filepath.Join(t.TempDir(), "golem.sock")
	listenUnix(t, socket)

	started := false
	err := cli.EnsureServer(t.Context(), socket, func() error {
		started = true
		return nil
	}, os.Stderr)
	if err != nil {
		t.Fatalf("EnsureServer: %v", err)
	}
	if started {
		t.Error("start called although server was already running")
	}
}

func TestEnsureServer_StartsWhenDown(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	socket := filepath.Join(dir, "golem.sock")

	err := cli.EnsureServer(t.Context(), socket, func() error {
		listenUnix(t, socket)
		return nil
	}, os.Stderr)
	if err != nil {
		t.Fatalf("EnsureServer: %v", err)
	}
	if !cli.Probe(socket) {
		t.Error("socket not reachable after EnsureServer")
	}
}

func TestEnsureServer_RemovesStaleSocketBeforeStart(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	socket := filepath.Join(dir, "golem.sock")

	stale, err := os.Create(socket)
	if err != nil {
		t.Fatalf("create stale file: %v", err)
	}
	stale.Close()

	// The listener bind fails with EADDRINUSE if the stale file survives
	// until start runs, so this doubles as the removal assertion.
	err = cli.EnsureServer(t.Context(), socket, func() error {
		listenUnix(t, socket)
		return nil
	}, os.Stderr)
	if err != nil {
		t.Fatalf("EnsureServer with stale socket file: %v", err)
	}
}

func TestEnsureServer_NotReadyErrors(t *testing.T) {
	t.Parallel()

	socket := filepath.Join(t.TempDir(), "golem.sock")

	ctx, cancel := context.WithTimeout(t.Context(), 300*time.Millisecond)
	defer cancel()

	err := cli.EnsureServer(ctx, socket, func() error { return nil }, os.Stderr)
	if err == nil || !strings.Contains(err.Error(), "not ready") {
		t.Errorf("expected not-ready error, got: %v", err)
	}
}

func TestEnsureServer_StartFailureBubbles(t *testing.T) {
	t.Parallel()

	socket := filepath.Join(t.TempDir(), "golem.sock")
	sentinel := errors.New("spawn refused")

	err := cli.EnsureServer(t.Context(), socket, func() error { return sentinel }, os.Stderr)
	if !errors.Is(err, sentinel) {
		t.Errorf("expected spawn error to bubble, got: %v", err)
	}
}

func TestSpawnServe_StartsExecutable(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	script := filepath.Join(dir, "fake-golem")
	if err := os.WriteFile(script, []byte("#!/bin/sh\nexit 0\n"), 0o755); err != nil {
		t.Fatalf("write script: %v", err)
	}

	if err := cli.SpawnServe(script); err != nil {
		t.Errorf("SpawnServe(script) = %v, want nil", err)
	}

	if err := cli.SpawnServe(""); err == nil {
		t.Error("SpawnServe(empty) = nil, want error")
	}
}
