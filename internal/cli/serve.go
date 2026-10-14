package cli

import (
	"context"
	"fmt"
	"net"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"syscall"

	"github.com/dev-hann/golem/internal/llm"
	"github.com/dev-hann/golem/internal/server"
	"github.com/dev-hann/golem/internal/session"
)

// ServeOptions configures a golem server run.
type ServeOptions struct {
	Provider   llm.Provider
	ProviderID string
	Model      string
	Root       string
	Socket     string
	Sessions   string
	Store      *session.Store
	// Version labels the build for client freshness checks.
	Version   string
	OnReady   func(addr string)
	SignalCtx func(ctx context.Context, stop context.CancelFunc)
}

// Serve runs the agent server until ctx is cancelled (default: SIGINT).
// It is the body of `golem serve`.
func Serve(ctx context.Context, opts ServeOptions) error {
	store := opts.Store
	if store == nil {
		var err error

		store, err = session.NewStore(opts.Sessions, defaultSessionCache)
		if err != nil {
			return fmt.Errorf("cli: session store: %w", err)
		}
	}

	srv, err := server.New(server.Config{
		Provider:   opts.Provider,
		ProviderID: opts.ProviderID,
		Model:      opts.Model,
		Root:       opts.Root,
		Store:      store,
		Version:    opts.Version,
	})
	if err != nil {
		return fmt.Errorf("cli: server: %w", err)
	}

	listener, err := (&net.ListenConfig{}).Listen(context.Background(), "unix", opts.Socket)
	if err != nil {
		return fmt.Errorf("cli: listening on %s: %w", opts.Socket, err)
	}

	defer func() { _ = listener.Close() }()

	_ = os.Chmod(opts.Socket, 0o600) //nolint:gosec // single-user local socket

	// The pid file lets a newer launcher restart this server on version
	// skew; it is removed on shutdown only when still ours so a successor
	// server's file survives the overlap.
	pidPath := opts.Socket + pidSuffix

	pid := strconv.Itoa(os.Getpid())

	if err := os.WriteFile(pidPath, []byte(pid), 0o600); err != nil { //nolint:gosec // golem's own pid path
		return fmt.Errorf("cli: writing pid file %s: %w", pidPath, err)
	}

	defer func() { removePIDFileIfMine(pidPath) }()

	// shutdown unblocks Serve: closing s.closed alone is not enough while
	// the accept loop is parked in Accept, so the listener must close too.
	shutdown := func() {
		srv.Close()
		_ = listener.Close()
	}

	if opts.SignalCtx != nil {
		opts.SignalCtx(ctx, shutdown)
	} else {
		sigCtx, stop := signal.NotifyContext(ctx, os.Interrupt, syscall.SIGTERM)
		defer stop()

		go func() {
			<-sigCtx.Done()
			shutdown()
		}()
	}

	if opts.OnReady != nil {
		opts.OnReady(opts.Socket)
	}

	if err := srv.Serve(listener); err != nil {
		return fmt.Errorf("cli: serve: %w", err)
	}

	return nil
}

// defaultSessionCache matches docs/CONFIG.md [server].session_cache.
const defaultSessionCache = 8

// pidSuffix names the pid file that accompanies a server socket.
const pidSuffix = ".pid"

// removePIDFileIfMine deletes the pid file when it still records this
// process, so a successor server started during shutdown keeps its own.
func removePIDFileIfMine(pidPath string) {
	raw, err := os.ReadFile(pidPath) //nolint:gosec // derived from the user's own socket path
	if err != nil {
		return
	}

	if strings.TrimSpace(string(raw)) == strconv.Itoa(os.Getpid()) {
		_ = os.Remove(pidPath)
	}
}
