package cli

import (
	"context"
	"fmt"
	"net"
	"os"
	"os/signal"
	"syscall"

	"github.com/dev-hann/golem/internal/llm"
	"github.com/dev-hann/golem/internal/server"
	"github.com/dev-hann/golem/internal/session"
)

// ServeOptions configures a golem server run.
type ServeOptions struct {
	Provider  llm.Provider
	Model     string
	Root      string
	Socket    string
	Sessions  string
	Store     *session.Store
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
		Provider: opts.Provider,
		Model:    opts.Model,
		Root:     opts.Root,
		Store:    store,
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

	if opts.SignalCtx != nil {
		opts.SignalCtx(ctx, func() { srv.Close() })
	} else {
		sigCtx, stop := signal.NotifyContext(ctx, os.Interrupt, syscall.SIGTERM)
		defer stop()

		go func() {
			<-sigCtx.Done()
			srv.Close()
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
