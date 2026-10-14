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
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/dev-hann/golem/pkg/golemclient"
)

// Launcher timings: dial probe budget, readiness re-check interval after a
// spawn, and the overall startup budget before giving up.
const (
	probeTimeout    = 500 * time.Millisecond
	pollInterval    = 100 * time.Millisecond
	readyTimeout    = 5 * time.Second
	shutdownTimeout = 3 * time.Second
)

// AppOptions configures the default app launch (bare `golem` / `golem tui`).
type AppOptions struct {
	Socket string
	// ConfigPath is checked for first-run onboarding.
	ConfigPath string
	// Version labels this build for the update check ("" skips it).
	Version string
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

// ServerVersion asks the server for its build version. It returns "" when
// nothing listens or the server predates the version handshake (which the
// caller treats as stale).
func ServerVersion(socket string) string {
	client, err := golemclient.Dial(socket)
	if err != nil {
		return ""
	}

	defer client.Close() //nolint:errcheck // probe connection

	if err := client.Send(golemclient.VersionQuery{Type: golemclient.TypeVersionQuery}); err != nil {
		return ""
	}

	ev, got, err := client.ReceiveWithTimeout(probeTimeout)
	if err != nil || !got {
		return ""
	}

	hello, ok := ev.(golemclient.ServerHello)
	if !ok {
		return ""
	}

	return hello.Version
}

// EnsureFreshServer guarantees the server on socket runs the client's own
// build: a version mismatch (or a server that cannot answer the handshake)
// triggers a restart — via the pid file, or by resolving the socket's
// owner with lsof. When neither yields a safe kill target the launch
// fails fast with the manual restart instruction; silently continuing on
// an outdated server hides features and is worse than a hard stop.
func EnsureFreshServer(
	ctx context.Context, socket, clientVersion string, start func() error, stderr io.Writer,
) error {
	if v := ServerVersion(socket); v == clientVersion {
		return nil
	}

	pid, ok := readPIDFile(socket + pidSuffix)
	if !ok {
		// No pid file (a pre-pidfile server): resolve the socket's owner
		// instead. SocketOwnerPID refuses self, so an embedded listener
		// falls through to the manual-restart error below.
		pid, ok = SocketOwnerPID(socket)
	}

	if !ok || pid <= 1 {
		return fmt.Errorf(
			"cli: outdated server on %s (server %q, client %q) — restart it: pkill -f 'golem serve'",
			socket, ServerVersion(socket), clientVersion,
		)
	}

	fmt.Fprintf(stderr, "golem: restarting outdated server (pid %d)\n", pid) //nolint:errcheck // terminal output

	if err := syscall.Kill(pid, syscall.SIGTERM); err != nil && !errors.Is(err, syscall.ESRCH) {
		return fmt.Errorf("cli: stopping server pid %d: %w", pid, err)
	}

	deadline := time.Now().Add(shutdownTimeout)

	for Probe(socket) && time.Now().Before(deadline) {
		timer := time.NewTimer(pollInterval)

		select {
		case <-ctx.Done():
			timer.Stop()

			return fmt.Errorf("cli: waiting for old server: %w", ctx.Err())
		case <-timer.C:
		}
	}

	return EnsureServer(ctx, socket, start, stderr)
}

// SocketOwnerPID resolves which process listens on the unix socket via
// lsof (`lsof -F pn -- <socket>`). It refuses this process and non-ids so
// a launcher can never kill itself; false means lsof is unavailable or
// the owner was not found.
func SocketOwnerPID(socket string) (int, bool) {
	//nolint:gosec,noctx // fixed short-lived probe binary; socket path is the user's own
	out, err := exec.Command("lsof", "-F", "pn", "--", socket).Output()
	if err != nil {
		return 0, false
	}

	pid := 0

	for line := range strings.SplitSeq(string(out), "\n") {
		switch {
		case strings.HasPrefix(line, "p"):
			parsed, parseErr := strconv.Atoi(line[1:])
			if parseErr != nil {
				return 0, false
			}

			pid = parsed
		case strings.HasPrefix(line, "n") && strings.TrimPrefix(line, "n") == socket:
			if pid > 1 && pid != os.Getpid() {
				return pid, true
			}

			return 0, false
		}
	}

	return 0, false
}

// readPIDFile returns the recorded server pid, if present and parsable.
func readPIDFile(pidPath string) (int, bool) {
	raw, err := os.ReadFile(pidPath) //nolint:gosec // derived from the user's own socket path
	if err != nil {
		return 0, false
	}

	pid, err := strconv.Atoi(strings.TrimSpace(string(raw)))
	if err != nil || pid <= 0 {
		return 0, false
	}

	return pid, true
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
			return fmt.Errorf(
				"cli: server on %s not ready: %w — details: %s (or run: golem serve)",
				socket, err, ServeLogPath(),
			)
		}

		if time.Now().After(deadline) {
			return fmt.Errorf(
				"cli: server on %s not ready within %s — details: %s (or run: golem serve)",
				socket, readyTimeout, ServeLogPath(),
			)
		}

		timer := time.NewTimer(pollInterval)
		select {
		case <-ctx.Done():
			timer.Stop()

			return fmt.Errorf(
				"cli: server on %s not ready: %w — details: %s (or run: golem serve)",
				socket, ctx.Err(), ServeLogPath(),
			)
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

// ServeLogPath is where the spawned daemon's output lands so startup
// failures are never invisible.
func ServeLogPath() string {
	return filepath.Join(dataDir(), "serve.log")
}

// SpawnServe starts exe as a detached `golem serve` process, logging its
// output to logPath. Production callers pass os.Executable()'s result so
// golem re-executes itself; the server outlives the client that spawned it.
func SpawnServe(exe, logPath string) error {
	if exe == "" {
		return fmt.Errorf("cli: spawn: no executable path")
	}

	if err := os.MkdirAll(filepath.Dir(logPath), 0o750); err != nil {
		return fmt.Errorf("cli: spawn: creating log dir: %w", err)
	}

	logFile, err := os.OpenFile(logPath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600) //nolint:gosec // golem's own log path
	if err != nil {
		return fmt.Errorf("cli: spawn: opening %s: %w", logPath, err)
	}

	cmd := exec.CommandContext(context.Background(), exe, "serve")
	cmd.Stdout = logFile
	cmd.Stderr = logFile
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true} // detach from the client's session

	if err := cmd.Start(); err != nil {
		_ = logFile.Close()

		return fmt.Errorf("cli: spawning %s: %w", exe, err)
	}

	// The daemon holds its own descriptor; close ours in this process.
	go func() {
		_ = cmd.Wait() //nolint:errcheck // daemon exit is observed via serve.log
		_ = logFile.Close()
	}()

	return nil
}

// RunApp is the body of bare `golem`: run the first-run setup wizard when
// no provider is configured, ensure the server is up (spawning it when
// needed), then run the TUI against it.
func RunApp(ctx context.Context, opts AppOptions) error {
	if NeedsOnboarding(opts.ConfigPath) {
		if err := RunOnboard(OnboardOptions{ConfigPath: opts.ConfigPath}); err != nil {
			return fmt.Errorf("cli: setup: %w", err)
		}
	}

	if opts.Version != "" {
		MaybeNotifyUpdate(ctx, opts.Stderr, opts.Version, ReleasesAPI)
	}

	if err := EnsureServer(ctx, opts.Socket, opts.Start, opts.Stderr); err != nil {
		return err
	}

	if opts.Version != "" {
		if err := EnsureFreshServer(ctx, opts.Socket, opts.Version, opts.Start, opts.Stderr); err != nil {
			return err
		}
	}

	return RunTUI(opts.Socket)
}
