// Package server implements the golem agent server: a unix-socket listener
// speaking the NDJSON protocol from docs/PROTOCOL.md, wiring sessions, the
// agent loop, and tools together.
package server

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"strings"
	"sync"
	"time"

	"github.com/oklog/ulid/v2"

	"github.com/dev-hann/golem/internal/agent"
	"github.com/dev-hann/golem/internal/llm"
	"github.com/dev-hann/golem/internal/session"
	"github.com/dev-hann/golem/internal/tools"
	"github.com/dev-hann/golem/pkg/golemclient"
)

// Config wires a Server's dependencies.
type Config struct {
	Provider llm.Provider
	// ProviderID labels turn_start events (e.g. "zai"); "" renders as
	// "custom" for test-injected providers.
	ProviderID string
	Model      string
	Root       string
	Store      *session.Store
	// Version labels this build for client freshness checks; "" hides
	// the server from version handshake (development default).
	Version string
}

// Server accepts client connections and runs agent turns.
type Server struct {
	cfg      Config
	registry *tools.Registry

	mu             sync.Mutex
	turns          map[string]*turnState
	modelOverrides map[string]string // session → model override (SetModel)

	closeOnce sync.Once
	closed    chan struct{}
}

// turnState tracks one active turn per session.
type turnState struct {
	ctx    context.Context
	cancel context.CancelFunc
	done   chan struct{} // closed by endTurn; delete_session waits on it
}

// New builds a Server.
func New(cfg Config) (*Server, error) {
	registry, err := tools.NewRegistry(
		tools.ReadTool{Root: cfg.Root},
		tools.WriteTool{Root: cfg.Root},
		tools.EditTool{Root: cfg.Root},
		tools.BashTool{Root: cfg.Root},
		tools.GrepTool{Root: cfg.Root},
		tools.GlobTool{Root: cfg.Root},
		tools.WebFetchTool{},
	)
	if err != nil {
		return nil, fmt.Errorf("server: building tools: %w", err)
	}

	return &Server{
		cfg:            cfg,
		registry:       registry,
		turns:          make(map[string]*turnState),
		modelOverrides: make(map[string]string),
		closed:         make(chan struct{}),
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

// Registry exposes the tool registry (for tests and diagnostics).
func (s *Server) Registry() *tools.Registry {
	return s.registry
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
	case golemclient.Interrupt:
		s.handleInterrupt(ev)
	case golemclient.SetModel:
		s.handleSetModel(writer, ev)
	case golemclient.RenameSession:
		s.handleRenameSession(writer, ev)
	case golemclient.DeleteSession:
		s.handleDeleteSession(writer, ev)
	case golemclient.CompactSession:
		s.handleCompact(writer, ev)
	case golemclient.VersionQuery:
		writer.send(golemclient.ServerHello{
			Type: golemclient.TypeServerHello, Version: s.cfg.Version,
		})
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

	writer.send(metaOf(sess, false))
}

// handleList replies with the session list.
func (s *Server) handleList(writer *connWriter) {
	summaries, err := s.cfg.Store.List()
	if err != nil {
		writer.send(errorOf("", "", "internal", err))
		return
	}

	busy := s.busySessions()

	list := golemclient.SessionList{Type: golemclient.TypeSessionList}
	for _, sum := range summaries {
		list.Sessions = append(list.Sessions, golemclient.SessionSummary{
			ID: sum.ID, Title: sum.Title, Cwd: sum.Cwd, Created: sum.Created.UTC().Format(rfc3339),
			Turns: sum.Turns, LastActive: sum.LastActive.UTC().Format(rfc3339),
			Busy: busy[sum.ID],
		})
	}

	writer.send(list)
}

// busySessions snapshots the ids with a turn currently running — the
// live state behind session_list's busy flags.
func (s *Server) busySessions() map[string]bool {
	s.mu.Lock()
	defer s.mu.Unlock()

	busy := make(map[string]bool, len(s.turns))
	for id := range s.turns {
		busy[id] = true
	}

	return busy
}

// handleLoad replays a session as meta plus history. The meta carries
// the session's live busy state so a client resuming mid-turn can
// restore its status line from the replay.
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

	writer.send(metaOf(sess, s.sessionBusy(sess.ID)))
	writer.send(golemclient.History{Type: golemclient.TypeHistory, Session: sess.ID, Events: raw})
}

// sessionBusy reports whether a turn currently runs in the session.
func (s *Server) sessionBusy(sessionID string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()

	_, busy := s.turns[sessionID]

	return busy
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

// handleInterrupt cancels the active turn for the session.
func (s *Server) handleInterrupt(ev golemclient.Interrupt) {
	s.cancelTurn(ev.Session)
}

// cancelTurn cancels the session's active turn, if any.
func (s *Server) cancelTurn(sessionID string) {
	s.mu.Lock()
	state := s.turns[sessionID]
	s.mu.Unlock()

	if state != nil {
		state.cancel()
	}
}

// handleSetModel stores a per-session model override applied from the next
// turn on. Empty models are rejected; unknown sessions error out.
func (s *Server) handleSetModel(writer *connWriter, ev golemclient.SetModel) {
	if _, err := s.cfg.Store.Get(ev.Session); err != nil {
		writer.send(errorOf(ev.Session, "", "session_not_found", err))

		return
	}

	model := strings.TrimSpace(ev.Model)
	if model == "" {
		writer.send(errorOf(ev.Session, "", "invalid_request",
			errors.New("set_model: model must not be empty")))

		return
	}

	s.mu.Lock()
	s.modelOverrides[ev.Session] = model
	s.mu.Unlock()
}

// handleRenameSession replaces a session's title: sanitized, persisted as
// the newest session_title record (last one wins on fold), and confirmed
// back to the requester.
func (s *Server) handleRenameSession(writer *connWriter, ev golemclient.RenameSession) {
	if _, err := s.cfg.Store.Get(ev.Session); err != nil {
		writer.send(errorOf(ev.Session, "", "session_not_found", err))

		return
	}

	title := sanitizeTitle(ev.Title)
	if title == "" {
		writer.send(errorOf(ev.Session, "", "invalid_request",
			errors.New("rename_session: title must not be empty")))

		return
	}

	if err := s.cfg.Store.Append(ev.Session, golemclient.SessionTitle{
		Type: golemclient.TypeSessionTitle, Session: ev.Session, Title: title,
	}); err != nil {
		writer.send(errorOf(ev.Session, "", "internal", err))

		return
	}

	s.cfg.Store.Invalidate(ev.Session)

	writer.send(golemclient.SessionTitle{
		Type: golemclient.TypeSessionTitle, Session: ev.Session, Title: title,
	})
}

// deleteTurnWait bounds how long delete_session waits for a cancelled
// turn's goroutine to drain before removing the file anyway. Draining is
// the rule (the cancel propagates through the provider stream); the cap
// covers a wedged provider.
const deleteTurnWait = 10 * time.Second

// handleDeleteSession removes a session, opencode-style: cancel a running
// turn first, wait for its goroutine to stop appending, then delete the
// file and confirm with session_deleted. The wait-then-delete ordering
// keeps a turn's Appends from racing the remove; Delete's permanent flag
// (ADR-0019) then refuses any late writer — a turn goroutine that
// outlived the drain bound, or the title side call — so the file cannot
// come back.
func (s *Server) handleDeleteSession(writer *connWriter, ev golemclient.DeleteSession) {
	if _, err := s.cfg.Store.Get(ev.Session); err != nil {
		writer.send(errorOf(ev.Session, "", "session_not_found", err))

		return
	}

	s.mu.Lock()
	state := s.turns[ev.Session]
	s.mu.Unlock()

	if state != nil {
		state.cancel()

		timer := time.NewTimer(deleteTurnWait)
		defer timer.Stop()

		select {
		case <-state.done:
		case <-timer.C:
		}
	}

	if err := s.cfg.Store.Delete(ev.Session); err != nil {
		writer.send(errorOf(ev.Session, "", "internal", err))

		return
	}

	s.mu.Lock()
	delete(s.modelOverrides, ev.Session)
	s.mu.Unlock()

	writer.send(golemclient.SessionDeleted{
		Type: golemclient.TypeSessionDeleted, Session: ev.Session,
	})
}

// turnModel resolves the model a session's next turn runs with.
func (s *Server) turnModel(sessionID string) string {
	s.mu.Lock()
	defer s.mu.Unlock()

	if model, ok := s.modelOverrides[sessionID]; ok {
		return model
	}

	return s.cfg.Model
}

// beginTurn registers an active turn; false when one already runs.
func (s *Server) beginTurn(ctx context.Context, sessionID string, cancel context.CancelFunc) bool {
	s.mu.Lock()
	defer s.mu.Unlock()

	if _, busy := s.turns[sessionID]; busy {
		return false
	}

	s.turns[sessionID] = &turnState{ctx: ctx, cancel: cancel, done: make(chan struct{})}

	return true
}

// endTurn unregisters the active turn. Closing done wakes delete_session
// waiters; the map delete guarantees this runs at most once per turn.
func (s *Server) endTurn(sessionID string) {
	s.mu.Lock()
	state := s.turns[sessionID]
	delete(s.turns, sessionID)
	s.mu.Unlock()

	if state != nil {
		close(state.done)
	}
}

// rfc3339 is the timestamp format used on the wire.
const rfc3339 = "2006-01-02T15:04:05Z07:00"

// newTurnID mints a ULID for a turn id.
func newTurnID() string {
	return ulid.Make().String()
}

// runTurn executes the agent loop, persisting then emitting each event.
func (s *Server) runTurn(ctx context.Context, writer *connWriter, msg golemclient.UserMessage) {
	defer s.endTurn(msg.Session)

	turnID := newTurnID()

	defer s.cfg.Store.Invalidate(msg.Session)

	model := s.turnModel(msg.Session)

	sess, err := s.cfg.Store.Get(msg.Session)
	if err != nil {
		writer.send(errorOf(msg.Session, turnID, "internal", err))
		return
	}

	// AGENTS.md discovery (ADR-0020): fold the workspace's instruction
	// files into the system prompt for this turn.
	ag := agent.New(s.cfg.Provider, s.registry, model,
		agent.WithInstructions(agent.DiscoverInstructions(sess.Cwd)))

	firstTurn := sess.Turns == 0 && sess.Title == ""

	// The title side call retries on later turns until one lands: every
	// turn of an untitled session gets a chance (the first user message
	// names the task regardless of which turn succeeds).
	needTitle := sess.Title == ""

	titleText := sess.FirstUserText
	if firstTurn {
		titleText = msg.Text
	}

	for event := range ag.Run(ctx, sess.Messages, msg.Text) {
		wire := s.wireEvent(msg.Session, turnID, model, event)
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

	if needTitle && titleText != "" {
		//nolint:gosec // the title side call must outlive the request-scoped turn context
		go s.generateTitle(msg.Session, titleText, model)
	}
}

// wireEvent converts one agent event to its protocol shape.
func (s *Server) wireEvent(sessionID, turnID, model string, event agent.Event) any {
	switch event.Type {
	case agent.EventTurnStart:
		return golemclient.TurnStart{
			Type: golemclient.TypeTurnStart, Session: sessionID, Turn: turnID,
			Provider: providerLabel(s.cfg.ProviderID), Model: model,
		}
	case agent.EventTextDelta:
		return golemclient.TextDelta{
			Type: golemclient.TypeTextDelta, Session: sessionID, Turn: turnID, Delta: event.Delta,
		}
	case agent.EventThinkingDelta:
		return golemclient.ThinkingDelta{
			Type: golemclient.TypeThinkingDelta, Session: sessionID, Turn: turnID,
		}
	case agent.EventRetry:
		return golemclient.Retry{
			Type: golemclient.TypeRetry, Session: sessionID, Turn: turnID,
			Attempt: event.Attempt, Retries: event.Retries, DelayMs: event.DelayMs,
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

// metaOf converts a session to wire metadata; busy carries the session's
// live turn state (load-time snapshot, false on create).
func metaOf(sess *session.Session, busy bool) golemclient.SessionMeta {
	return golemclient.SessionMeta{
		Type: golemclient.TypeSessionMeta, Session: sess.ID, Cwd: sess.Cwd,
		Created: sess.Created.UTC().Format(rfc3339), Turns: sess.Turns, Busy: busy,
	}
}

// providerLabel names the provider for turn_start events; unknown or
// test-injected providers render as "custom".
func providerLabel(id string) string {
	if id == "" {
		return "custom"
	}

	return id
}

// firstNonEmpty returns the first non-empty string.
func firstNonEmpty(a, b string) string {
	if a != "" {
		return a
	}

	return b
}
