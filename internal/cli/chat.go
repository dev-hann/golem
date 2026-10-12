package cli

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"strings"
	"sync"
	"sync/atomic"

	"github.com/dev-hann/golem/pkg/golemclient"
)

// ChatOptions configures a line-REPL chat client run.
type ChatOptions struct {
	Socket string
	Stdin  io.Reader
	Stdout io.Writer
	Stderr io.Writer
}

// syncWriter serializes writes between the event renderer and the main
// loop's shutdown path.
type syncWriter struct {
	mu sync.Mutex
	w  io.Writer
}

// Write implements io.Writer under the lock.
func (s *syncWriter) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	return s.w.Write(p)
}

// printf writes a formatted line under the lock. Terminal write errors are
// unhandleable here and deliberately ignored.
func (s *syncWriter) printf(format string, a ...any) {
	s.mu.Lock()
	defer s.mu.Unlock()

	fmt.Fprintf(s.w, format, a...) //nolint:errcheck // terminal output
}

// print writes raw text under the lock, ignoring terminal write errors.
func (s *syncWriter) print(text string) {
	s.mu.Lock()
	defer s.mu.Unlock()

	fmt.Fprint(s.w, text) //nolint:errcheck // terminal output
}

// Chat runs a line-based REPL against a golem server: the body of
// `golem chat`. It exits when stdin ends, /quit is entered, or ctx is
// cancelled. Approval requests are auto-denied in this phase (interactive
// approval UI is roadmap phase 4).
func Chat(ctx context.Context, opts ChatOptions) error {
	client, err := golemclient.Dial(opts.Socket)
	if err != nil {
		return fmt.Errorf("cli: %w", err)
	}

	stdout := &syncWriter{w: opts.Stdout}
	stderr := &syncWriter{w: opts.Stderr}

	var quiet atomic.Bool

	defer func() {
		stderr.mu.Lock()
		quiet.Store(true)
		stderr.mu.Unlock()

		_ = client.Close()
	}()

	if err := client.Send(golemclient.CreateSession{Type: golemclient.TypeCreateSession}); err != nil {
		return fmt.Errorf("cli: creating session: %w", err)
	}

	events := make(chan any, chatEventBuffer)
	go receiveEvents(ctx, client, events, stderr, &quiet)

	sessionID := waitSessionMeta(ctx, events, stdout, stderr)
	if sessionID == "" {
		return fmt.Errorf("cli: connection closed before session creation")
	}

	scanner := bufio.NewScanner(opts.Stdin)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}

		if line == "/quit" || line == "/exit" {
			return nil
		}

		if err := client.Send(golemclient.UserMessage{
			Type: golemclient.TypeUserMessage, Session: sessionID, Text: line,
		}); err != nil {
			return fmt.Errorf("cli: sending: %w", err)
		}

		if err := drainTurn(ctx, events, stdout, stderr, client, sessionID); err != nil {
			return err
		}
	}

	if err := scanner.Err(); err != nil {
		return fmt.Errorf("cli: reading input: %w", err)
	}

	return nil
}

// chatEventBuffer sizes the internal event channel.
const chatEventBuffer = 64

// waitSessionMeta consumes events until the session meta arrives, returning
// the session id ("" if the stream ended first).
func waitSessionMeta(ctx context.Context, events <-chan any, stdout, stderr *syncWriter) string {
	for {
		select {
		case <-ctx.Done():
			return ""
		case ev, ok := <-events:
			if !ok {
				return ""
			}

			switch e := ev.(type) {
			case golemclient.SessionMeta:
				stdout.printf("session %s — type /quit to exit\n", e.Session)
				return e.Session
			case golemclient.ErrorEvent:
				stderr.printf("error[%s]: %s\n", e.Code, e.Message)
			default:
				// ignore other pre-session events
			}
		}
	}
}

// receiveEvents pumps decoded server events into ch until the connection
// ends.
func receiveEvents(
	ctx context.Context,
	client *golemclient.Client,
	ch chan<- any,
	stderr *syncWriter,
	quiet *atomic.Bool,
) {
	for {
		ev, err := client.Receive()
		if err != nil {
			stderr.mu.Lock()
			if !quiet.Load() {
				select {
				case <-ctx.Done():
				default:
					stderr.printf("connection closed: %v\n", err)
				}
			}
			stderr.mu.Unlock()
			close(ch)

			return
		}

		ch <- ev
	}
}

// drainTurn renders events until the active turn ends (turn_end or error).
func drainTurn(
	ctx context.Context,
	events <-chan any,
	stdout, stderr *syncWriter,
	client *golemclient.Client,
	sessionID string,
) error {
	for {
		select {
		case <-ctx.Done():
			return nil
		case ev, ok := <-events:
			if !ok {
				return fmt.Errorf("cli: connection closed")
			}

			done, err := renderChatEvent(stdout, stderr, client, sessionID, ev)
			if err != nil {
				return err
			}

			if done {
				return nil
			}
		}
	}
}

// renderChatEvent prints one event; returns done=true at turn end. Approval
// requests are auto-denied (interactive approval arrives in phase 4).
func renderChatEvent(
	stdout, stderr *syncWriter,
	client *golemclient.Client,
	sessionID string,
	ev any,
) (bool, error) {
	switch e := ev.(type) {
	case golemclient.TextDelta:
		stdout.print(e.Delta)
	case golemclient.TurnStart:
		stdout.print("\n")
	case golemclient.ToolCallStart:
		stderr.printf("→ %s %s\n", e.Tool, string(e.Args))
	case golemclient.ToolCallEnd:
		status := "✓"
		if !e.OK {
			status = "✗"
		}

		stderr.printf("%s %s\n", status, e.CallID)
	case golemclient.ApprovalRequest:
		stderr.printf("approval needed: %s — auto-denied (interactive approval is phase 4)\n", e.Preview)
		err := client.Send(golemclient.ApprovalResponse{
			Type: golemclient.TypeApprovalResponse, Session: sessionID,
			Turn: e.Turn, ApprovalID: e.ApprovalID, Decision: "deny",
		})

		return false, err
	case golemclient.ErrorEvent:
		stderr.printf("error[%s]: %s\n", e.Code, e.Message)
		return true, nil
	case golemclient.TurnEnd:
		stderr.printf("\n[tokens: in=%d out=%d]\n", e.Usage.InputTokens, e.Usage.OutputTokens)
		return true, nil
	default:
		// history/session events are not rendered in chat
	}

	return false, nil
}
