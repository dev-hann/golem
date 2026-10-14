package cli_test

import (
	"context"
	"errors"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/dev-hann/golem/internal/cli"
	"github.com/dev-hann/golem/internal/session"
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

	socket := filepath.Join(shortTempDir(t), "golem.sock")

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

	socket := filepath.Join(shortTempDir(t), "golem.sock")
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

	dir := shortTempDir(t)
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

	dir := shortTempDir(t)
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

	socket := filepath.Join(shortTempDir(t), "golem.sock")

	ctx, cancel := context.WithTimeout(t.Context(), 300*time.Millisecond)
	defer cancel()

	err := cli.EnsureServer(ctx, socket, func() error { return nil }, os.Stderr)
	if err == nil || !strings.Contains(err.Error(), "not ready") {
		t.Errorf("expected not-ready error, got: %v", err)
	}
	if !strings.Contains(err.Error(), "serve.log") {
		t.Errorf("not-ready error should point at the serve log, got: %v", err)
	}
}

func TestSpawnServe_ChildOutputLandsInLog(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	script := filepath.Join(dir, "talkative-golem")
	if err := os.WriteFile(script, []byte("#!/bin/sh\necho daemon-started\nexit 0\n"), 0o755); err != nil {
		t.Fatalf("write script: %v", err)
	}

	logPath := filepath.Join(dir, "logs", "serve.log")
	if err := cli.SpawnServe(script, logPath); err != nil {
		t.Fatalf("SpawnServe: %v", err)
	}

	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if body, err := os.ReadFile(logPath); err == nil && strings.Contains(string(body), "daemon-started") {
			return
		}

		time.Sleep(10 * time.Millisecond)
	}

	t.Fatalf("serve.log never captured child output: %v", logPath)
}

func TestRunOnboard_WarnsWhenServerAlreadyRunning(t *testing.T) {
	runtimeDir := shortTempDir(t)
	t.Setenv("XDG_RUNTIME_DIR", runtimeDir)
	clearProviderKeys(t)

	// A server is listening on the default socket while the wizard runs.
	listenUnix(t, filepath.Join(runtimeDir, "golem.sock"))

	h := newOnboard(t, "1\nsk-zai-123\n\n")

	if err := h.run(t); err != nil {
		t.Fatalf("RunOnboard: %v", err)
	}

	if !strings.Contains(h.stdout.String(), "already running") {
		t.Errorf("stale-server warning missing:\n%s", h.stdout.String())
	}
}

func TestEnsureServer_StartFailureBubbles(t *testing.T) {
	t.Parallel()

	socket := filepath.Join(shortTempDir(t), "golem.sock")
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

	if err := cli.SpawnServe(script, filepath.Join(dir, "serve.log")); err != nil {
		t.Errorf("SpawnServe(script) = %v, want nil", err)
	}

	if err := cli.SpawnServe("", "unused.log"); err == nil {
		t.Error("SpawnServe(empty) = nil, want error")
	}
}

// startServe runs a real cli.Serve on socket with the given version label.
func startServe(t *testing.T, socket, version string) {
	t.Helper()

	store, err := session.NewStore(filepath.Join(shortTempDir(t), "sessions"), 4)
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}

	ready := make(chan struct{})

	go func() {
		_ = cli.Serve(context.Background(), cli.ServeOptions{
			Provider: echoProvider{}, Model: "stub", Root: t.TempDir(),
			Socket: socket, Store: store, Version: version,
			OnReady: func(string) { close(ready) },
		})
	}()

	select {
	case <-ready:
	case <-time.After(5 * time.Second):
		t.Fatalf("serve %s not ready", version)
	}
}

func TestServerVersion(t *testing.T) {
	t.Parallel()

	socket := filepath.Join(shortTempDir(t), "golem.sock")

	if v := cli.ServerVersion(socket); v != "" {
		t.Errorf("ServerVersion(missing) = %q, want empty", v)
	}

	startServe(t, socket, "v-old")

	if v := cli.ServerVersion(socket); v != "v-old" {
		t.Errorf("ServerVersion = %q, want v-old", v)
	}
}

func TestEnsureFreshServer_RestartsStaleServer(t *testing.T) {
	t.Parallel()

	socket := filepath.Join(shortTempDir(t), "golem.sock")
	startServe(t, socket, "v-old")

	if err := cli.EnsureFreshServer(t.Context(), socket, "v-new", func() error {
		startServe(t, socket, "v-new")
		return nil
	}, os.Stderr); err != nil {
		t.Fatalf("EnsureFreshServer: %v", err)
	}

	if v := cli.ServerVersion(socket); v != "v-new" {
		t.Errorf("ServerVersion after restart = %q, want v-new", v)
	}
}

func TestSocketOwnerPIDExcludesSelf(t *testing.T) {
	t.Parallel()

	socket := filepath.Join(shortTempDir(t), "golem.sock")
	listenUnix(t, socket) // the listener is this test process itself

	if pid, ok := cli.SocketOwnerPID(socket); ok {
		t.Errorf("SocketOwnerPID(self-held) = %d, want refusal", pid)
	}
}

func TestSocketOwnerPIDFindsForeignListener(t *testing.T) {
	t.Parallel()

	socket := filepath.Join(shortTempDir(t), "golem.sock")

	// A child process holds the socket; the launcher must resolve its pid.
	cmd := exec.CommandContext(t.Context(), "python3", "-c",
		"import socket,time\ns=socket.socket(socket.AF_UNIX)\ns.bind('"+socket+"')\ns.listen(1)\ntime.sleep(30)")
	if err := cmd.Start(); err != nil {
		t.Skipf("python3 unavailable: %v", err)
	}

	t.Cleanup(func() { _ = cmd.Process.Kill() })

	deadline := time.Now().Add(3 * time.Second)

	for time.Now().Before(deadline) {
		if pid, ok := cli.SocketOwnerPID(socket); ok {
			if pid != cmd.Process.Pid {
				t.Errorf("SocketOwnerPID = %d, want child pid %d", pid, cmd.Process.Pid)
			}

			return
		}

		time.Sleep(50 * time.Millisecond)
	}

	t.Fatal("SocketOwnerPID never resolved the child listener")
}

func TestEnsureFreshServer_FailsFastWithoutKillPath(t *testing.T) {
	t.Parallel()

	socket := filepath.Join(shortTempDir(t), "golem.sock")
	listenUnix(t, socket) // held by this process: pidfile absent, lsof must refuse self

	started := false

	err := cli.EnsureFreshServer(t.Context(), socket, "v-new", func() error {
		started = true
		return nil
	}, os.Stderr)
	if err == nil {
		t.Fatal("EnsureFreshServer should fail fast when no safe kill path exists")
	}

	if started {
		t.Error("start must not run when the old server cannot be stopped")
	}

	if !strings.Contains(err.Error(), "pkill") {
		t.Errorf("error should carry the manual restart instruction: %v", err)
	}
}
