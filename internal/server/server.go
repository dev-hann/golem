// Package server implements the golem agent server: a unix-socket listener
// speaking the NDJSON protocol from docs/PROTOCOL.md, wiring sessions, the
// agent loop, tools, and the approval gate together.
package server

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"net"
	"sync"
	"time"

	"github.com/oklog/ulid/v2"

	"github.com/golem-agent/golem/internal/agent"
	"github.com/golem-agent/golem/internal/approval"
	"github.com/golem-agent/golem/internal/llm"
	"github.com/golem-agent/golem/internal/session"
	"github.com/golem-agent/golem/internal/tools"
	"github.com/golem-agent/golem/pkg/golemclient"
)

// defaultApprovalTimeout matches docs/CONFIG.md [approval].timeout_secs.
const defaultApprovalTimeout = 120 // seconds

// Config wires a Server's dependencies.
type Config struct {
	Provider         llm.Provider
	Model            string
	Root             string
	Store            *session.Store
	ApprovalTimeoutS int
}

// Server accepts client connections and runs agent turns.
type Server struct {
	cfg      Config
	registry *tools.Registry
	policy   *approval.Policy

	mu    sync.Mutex
	turns map[string]*turnState

	closeOnce sync.Once
	closed    chan struct{}
}

// turnState tracks one active turn per session.
type turnState struct {
	mu      sync.Mutex
	ctx     context.Context
	cancel  context.CancelFunc
	pending map[string]chan string // approvalID → decision
}

// New builds a Server.
func New(cfg Config) (*Server, error) {
	if cfg.ApprovalTimeoutS <= 0 {
		cfg.ApprovalTimeoutS = defaultApprovalTimeout
	}

	registry, err := tools.NewRegistry(
		tools.ReadTool{Root: cfg.Root},
		tools.WriteTool{Root: cfg.Root},
		tools.EditTool{Root: cfg.Root},
		tools.BashTool{Root: cfg.Root},
		tools.GrepTool{Root: cfg.Root},
		tools.GlobTool{Root: cfg.Root},
	)
	if err != nil {
		return nil, fmt.Errorf("server: building tools: %w", err)
	}

	return &Server{
		cfg:      cfg,
		registry: registry,
		policy:   approval.NewPolicy(approval.ModePrompt, approval.DefaultPrefixes),
		turns:    make(map[string]*turnState),
		closed:   make(chan struct{}),
	}, nil
}

// Serve accepts connections on l until Close.
func (s *Server) Serve(l net.Listener) error {
	for {
		conn, err := l.Accept()
		if err != nil {
			select {
			case <-s.closed:
				return nil
			default:
				return fmt.Errorf("server: accept: %w", err)
			}
		}

		go s.handleConn(conn)
	}
}

// Close stops accepting; active turns finish on their own contexts.
func (s *Server) Close() {
	s.closeOnce.Do(func() {
		close(s.closed)
	})
}

// handleConn reads NDJSON client events and dispatches them.
func (s *Server) handleConn(conn net.Conn) {
	defer conn.Close() //nolint:errcheck // best-effort close

	writer := newConnWriter(conn)
	scanner := bufio.NewScanner(conn)
	scanner.Buffer(make([]byte, 0, 64*1024), 4*1024*1024)

	for scanner.Scan() {
		event, err := golemclient.Decode(scanner.Bytes())
		if err != nil {
			writer.send(golemclient.ErrorEvent{
				Type: golemclient.TypeError, Code: "invalid_request",
				Message: "malformed event line",
			})

			continue
		}

		if stop := s.dispatch(conn, writer, event); stop {
			return
		}
	}
}

// dispatch routes one decoded client event. It returns true when the
// connection should close.
func (s *Server) dispatch(_ net.Conn, writer *connWriter, event any) bool {
	switch ev := event.(type) {
	case golemclient.CreateSession:
		s.handleCreate(writer, ev)
	case golemclient.ListSessions:
		s.handleList(writer)
	case golemclient.LoadSession:
		s.handleLoad(writer, ev)
	case golemclient.UserMessage:
		s.handleUserMessage(writer, ev)
	case golemclient.ApprovalResponse:
		s.handleApprovalResponse(ev)
	case golemclient.Interrupt:
		s.handleInterrupt(ev)
	case golemclient.Envelope:
		// Unknown types are ignored per PROTOCOL.md forward compatibility.
	default:
		// Decoded concrete events not handled above are ignored too.
	}

	return false
}

// handleCreate starts a session and replies with its meta.
func (s *Server) handleCreate(writer *connWriter, ev golemclient.CreateSession) {
	sess, err := s.cfg.Store.Create(firstNonEmpty(ev.Cwd, s.cfg.Root))
	if err != nil {
		writer.send(errorOf("", "", "internal", err))
		return
	}

	writer.send(metaOf(sess))
}

// handleList replies with the session list.
func (s *Server) handleList(writer *connWriter) {
	summaries, err := s.cfg.Store.List()
	if err != nil {
		writer.send(errorOf("", "", "internal", err))
		return
	}

	list := golemclient.SessionList{Type: golemclient.TypeSessionList}
	for _, sum := range summaries {
		list.Sessions = append(list.Sessions, golemclient.SessionSummary{
			ID: sum.ID, Cwd: sum.Cwd, Created: sum.Created.UTC().Format(rfc3339),
			Turns: sum.Turns, LastActive: sum.LastActive.UTC().Format(rfc3339),
		})
	}

	writer.send(list)
}

// handleLoad replays a session as meta plus history.
func (s *Server) handleLoad(writer *connWriter, ev golemclient.LoadSession) {
	sess, err := s.cfg.Store.Get(ev.Session)
	if err != nil {
		writer.send(errorOf(ev.Session, "", "session_not_found", err))
		return
	}

	raw, err := s.cfg.Store.RawEvents(ev.Session)
	if err != nil {
		writer.send(errorOf(ev.Session, "", "internal", err))
		return
	}

	writer.send(metaOf(sess))
	writer.send(golemclient.History{Type: golemclient.TypeHistory, Session: sess.ID, Events: raw})
}

// handleUserMessage runs one turn if the session is free.
func (s *Server) handleUserMessage(writer *connWriter, ev golemclient.UserMessage) {
	if _, err := s.cfg.Store.Get(ev.Session); err != nil {
		writer.send(errorOf(ev.Session, "", "session_not_found", err))
		return
	}

	turnCtx, cancel := context.WithCancel(context.Background())
	if !s.beginTurn(turnCtx, ev.Session, cancel) {
		cancel()
		writer.send(errorOf(ev.Session, "", "invalid_request",
			fmt.Errorf("a turn is already in progress for this session")))

		return
	}
	// Persist the user message before any turn events reference it.
	if err := s.cfg.Store.Append(ev.Session, ev); err != nil {
		cancel()
		s.endTurn(ev.Session)
		writer.send(errorOf(ev.Session, "", "internal", err))

		return
	}
	go s.runTurn(turnCtx, writer, ev)
}

// handleApprovalResponse routes a decision to the waiting turn.
func (s *Server) handleApprovalResponse(ev golemclient.ApprovalResponse) {
	s.mu.Lock()
	state := s.turns[ev.Session]
	s.mu.Unlock()

	if state == nil {
		return
	}

	state.mu.Lock()
	defer state.mu.Unlock()

	if ch, ok := state.pending[ev.ApprovalID]; ok {
		select {
		case ch <- ev.Decision:
		default:
		}
	}
}

// handleInterrupt cancels the active turn for the session.
func (s *Server) handleInterrupt(ev golemclient.Interrupt) {
	s.mu.Lock()
	state := s.turns[ev.Session]
	s.mu.Unlock()

	if state != nil {
		state.cancel()
	}
}

// beginTurn registers an active turn; false when one already runs.
func (s *Server) beginTurn(ctx context.Context, sessionID string, cancel context.CancelFunc) bool {
	s.mu.Lock()
	defer s.mu.Unlock()

	if _, busy := s.turns[sessionID]; busy {
		return false
	}

	s.turns[sessionID] = &turnState{ctx: ctx, cancel: cancel, pending: make(map[string]chan string)}

	return true
}

// endTurn unregisters the active turn.
func (s *Server) endTurn(sessionID string) {
	s.mu.Lock()
	defer s.mu.Unlock()

	delete(s.turns, sessionID)
}

// rfc3339 is the timestamp format used on the wire.
const rfc3339 = "2006-01-02T15:04:05Z07:00"

// newTurnID mints a ULID for a turn or approval id.
func newTurnID() string {
	return ulid.Make().String()
}

// timeoutFromSecs converts config seconds to a Duration.
func timeoutFromSecs(secs int) time.Duration {
	return time.Duration(secs) * time.Second
}

// runTurn executes the agent loop, persisting then emitting each event.
func (s *Server) runTurn(ctx context.Context, writer *connWriter, msg golemclient.UserMessage) {
	defer s.endTurn(msg.Session)

	turnID := newTurnID()

	defer s.cfg.Store.Invalidate(msg.Session)

	ag := agent.New(s.cfg.Provider, s.registry, s.policy, s.cfg.Model,
		agent.WithApprovalFunc(s.approver(writer, msg.Session, turnID)))

	sess, err := s.cfg.Store.Get(msg.Session)
	if err != nil {
		writer.send(errorOf(msg.Session, turnID, "internal", err))
		return
	}

	for event := range ag.Run(ctx, sess.Messages, msg.Text) {
		wire := s.wireEvent(msg.Session, turnID, event)
		if wire == nil {
			continue
		}
		// Persist before emit: a crash between the two leaves storage
		// ahead of the client, which replay repairs (ROADMAP phase 2).
		if err := s.cfg.Store.Append(msg.Session, wire); err != nil {
			writer.send(errorOf(msg.Session, turnID, "internal", err))
			return
		}

		writer.send(wire)
	}
}

// wireEvent converts one agent event to its protocol shape.
func (s *Server) wireEvent(sessionID, turnID string, event agent.Event) any {
	switch event.Type {
	case agent.EventTurnStart:
		return golemclient.TurnStart{
			Type: golemclient.TypeTurnStart, Session: sessionID, Turn: turnID,
			Provider: providerLabel(s.cfg.Provider), Model: s.cfg.Model,
		}
	case agent.EventTextDelta:
		return golemclient.TextDelta{
			Type: golemclient.TypeTextDelta, Session: sessionID, Turn: turnID, Delta: event.Delta,
		}
	case agent.EventToolCallStart:
		return golemclient.ToolCallStart{
			Type: golemclient.TypeToolCallStart, Session: sessionID, Turn: turnID,
			CallID: event.CallID, Tool: event.Tool, Args: event.Args,
		}
	case agent.EventToolCallEnd:
		return golemclient.ToolCallEnd{
			Type: golemclient.TypeToolCallEnd, Session: sessionID, Turn: turnID,
			CallID: event.CallID, OK: event.OK, Result: event.Result,
		}
	case agent.EventError:
		return errorOf(sessionID, turnID, "provider_error", event.Err)
	case agent.EventTurnEnd:
		return golemclient.TurnEnd{
			Type: golemclient.TypeTurnEnd, Session: sessionID, Turn: turnID,
			Usage: golemclient.Usage{InputTokens: event.InputTokens, OutputTokens: event.OutputTokens},
		}
	default:
		return nil
	}
}

// approver builds the interactive approval hook for one turn.
func (s *Server) approver(
	writer *connWriter,
	sessionID, turnID string,
) agent.ApprovalFunc {
	return func(ctx context.Context, call llm.ToolCall, preview string) bool {
		approvalID := "ap_" + newTurnID()[:8]
		ch := make(chan string, 1)

		s.mu.Lock()
		state := s.turns[sessionID]
		s.mu.Unlock()

		if state == nil {
			return false
		}

		state.mu.Lock()
		state.pending[approvalID] = ch

		state.mu.Unlock()
		defer func() {
			state.mu.Lock()
			delete(state.pending, approvalID)
			state.mu.Unlock()
		}()

		request := golemclient.ApprovalRequest{
			Type: golemclient.TypeApprovalRequest, Session: sessionID, Turn: turnID,
			ApprovalID: approvalID, CallID: call.ID, Tool: call.Name,
			Preview: preview + " (cwd: " + s.cfg.Root + ")",
		}
		if err := s.cfg.Store.Append(sessionID, request); err != nil {
			return false
		}

		writer.send(request)

		timeout := timeoutFromSecs(s.cfg.ApprovalTimeoutS)

		timer := time.AfterFunc(timeout, func() {
			select {
			case ch <- "deny":
			default:
			}
		})
		defer timer.Stop()

		select {
		case decision := <-ch:
			return decision == "allow"
		case <-ctx.Done():
			return false
		}
	}
}

// connWriter serializes event writes on one connection.
type connWriter struct {
	mu   sync.Mutex
	conn net.Conn
}

// newConnWriter builds a writer for a connection.
func newConnWriter(conn net.Conn) *connWriter {
	return &connWriter{conn: conn}
}

// send marshals and writes one event line.
func (w *connWriter) send(event any) {
	line, err := json.Marshal(event)
	if err != nil {
		return // unreachable: protocol types always marshal
	}

	w.mu.Lock()
	defer w.mu.Unlock()

	_, _ = w.conn.Write(append(line, '\n'))
}

// errorOf builds an ErrorEvent.
func errorOf(sessionID, turnID, code string, err error) golemclient.ErrorEvent {
	return golemclient.ErrorEvent{
		Type: golemclient.TypeError, Session: sessionID, Turn: turnID,
		Code: code, Message: err.Error(),
	}
}

// metaOf converts a session to wire metadata.
func metaOf(sess *session.Session) golemclient.SessionMeta {
	return golemclient.SessionMeta{
		Type: golemclient.TypeSessionMeta, Session: sess.ID, Cwd: sess.Cwd,
		Created: sess.Created.UTC().Format(rfc3339), Turns: sess.Turns,
	}
}

// providerLabel names the provider kind for turn_start events.
func providerLabel(_ llm.Provider) string {
	return "anthropic" // single provider in phase 2; extended in phase 5
}

// firstNonEmpty returns the first non-empty string.
func firstNonEmpty(a, b string) string {
	if a != "" {
		return a
	}

	return b
}
