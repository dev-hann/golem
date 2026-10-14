// Package golemclient is the public client library for golem servers. It
// defines the NDJSON wire protocol types (see docs/PROTOCOL.md) and a typed
// client usable by TUIs, CLIs, and future desktop/web frontends. It depends
// on the standard library only and must never import internal/*.
package golemclient

import (
	"encoding/json"
	"fmt"
)

// Event type names (client → server).
const (
	TypeUserMessage   = "user_message"
	TypeInterrupt     = "interrupt"
	TypeCreateSession = "create_session"
	TypeListSessions  = "list_sessions"
	TypeLoadSession   = "load_session"
	TypeRenameSession = "rename_session"
	TypeVersionQuery  = "version_query"
)

// Event type names (server → client).
const (
	TypeSessionMeta   = "session_meta"
	TypeHistory       = "history"
	TypeSessionList   = "session_list"
	TypeTurnStart     = "turn_start"
	TypeTextDelta     = "text_delta"
	TypeToolCallStart = "tool_call_start"
	TypeToolCallEnd   = "tool_call_end"
	TypeError         = "error"
	TypeTurnEnd       = "turn_end"
	TypeSessionTitle  = "session_title"
	TypeServerHello   = "server_hello"
)

// Envelope is the minimal shape every message shares. Unknown types must be
// ignored by both ends.
type Envelope struct {
	Type    string `json:"type"`
	Session string `json:"session,omitempty"`
	Turn    string `json:"turn,omitempty"`
}

// UserMessage is a client prompt that starts a turn.
type UserMessage struct {
	Type    string `json:"type"`
	Session string `json:"session"`
	Text    string `json:"text"`
}

// Interrupt cancels the active turn.
type Interrupt struct {
	Type    string `json:"type"`
	Session string `json:"session"`
	Turn    string `json:"turn,omitempty"`
}

// CreateSession asks the server for a new session.
type CreateSession struct {
	Type string `json:"type"`
	Cwd  string `json:"cwd,omitempty"`
}

// ListSessions requests the SessionList.
type ListSessions struct {
	Type string `json:"type"`
}

// LoadSession requests SessionMeta plus History replay.
type LoadSession struct {
	Type    string `json:"type"`
	Session string `json:"session"`
}

// SessionMeta announces a session (creation or load).
type SessionMeta struct {
	Type    string `json:"type"`
	Session string `json:"session"`
	Cwd     string `json:"cwd"`
	Created string `json:"created"`
	Turns   int    `json:"turns"`
}

// History replays stored events after LoadSession.
type History struct {
	Type    string            `json:"type"`
	Session string            `json:"session"`
	Events  []json.RawMessage `json:"events"`
}

// SessionSummary is one row in SessionList.
type SessionSummary struct {
	ID      string `json:"id"`
	Title   string `json:"title,omitempty"`
	Cwd     string `json:"cwd"`
	Created string `json:"created"`
	Turns   int    `json:"turns"`
	// LastActive is the session's most recent activity timestamp.
	LastActive string `json:"last_active"`
	// Busy reports a turn currently running in this session (live server
	// state; false on idle or replay).
	Busy bool `json:"busy"`
}

// SessionList enumerates sessions, most recently active first.
type SessionList struct {
	Type     string           `json:"type"`
	Sessions []SessionSummary `json:"sessions"`
}

// TurnStart begins a turn's event stream.
type TurnStart struct {
	Type     string `json:"type"`
	Session  string `json:"session"`
	Turn     string `json:"turn"`
	Provider string `json:"provider"`
	Model    string `json:"model"`
}

// TextDelta carries one assistant text chunk.
type TextDelta struct {
	Type    string `json:"type"`
	Session string `json:"session"`
	Turn    string `json:"turn"`
	Delta   string `json:"delta"`
}

// ToolCallStart announces a tool invocation.
type ToolCallStart struct {
	Type    string          `json:"type"`
	Session string          `json:"session"`
	Turn    string          `json:"turn"`
	CallID  string          `json:"call_id"`
	Tool    string          `json:"tool"`
	Args    json.RawMessage `json:"args"`
}

// ToolCallEnd reports a tool outcome. Failures put the error text in Result.
type ToolCallEnd struct {
	Type    string `json:"type"`
	Session string `json:"session"`
	Turn    string `json:"turn"`
	CallID  string `json:"call_id"`
	OK      bool   `json:"ok"`
	Result  string `json:"result"`
}

// ErrorEvent is a recoverable protocol-level error.
type ErrorEvent struct {
	Type    string `json:"type"`
	Session string `json:"session,omitempty"`
	Turn    string `json:"turn,omitempty"`
	Code    string `json:"code"`
	Message string `json:"message"`
}

// Usage reports token consumption for a turn.
type Usage struct {
	InputTokens  int64 `json:"input_tokens"`
	OutputTokens int64 `json:"output_tokens"`
}

// TurnEnd closes a turn's event stream.
type TurnEnd struct {
	Type    string `json:"type"`
	Session string `json:"session"`
	Turn    string `json:"turn"`
	Usage   Usage  `json:"usage"`
}

// SessionTitle is a server-generated short title for a session, derived
// from its first exchange. It is persisted as a session record and
// surfaced through SessionList.
type SessionTitle struct {
	Type    string `json:"type"`
	Session string `json:"session"`
	Title   string `json:"title"`
}

// RenameSession asks the server to replace a session's title.
type RenameSession struct {
	Type    string `json:"type"`
	Session string `json:"session"`
	Title   string `json:"title"`
}

// VersionQuery asks the server to identify its build (ServerHello).
type VersionQuery struct {
	Type string `json:"type"`
}

// ServerHello answers VersionQuery with the server's build version.
type ServerHello struct {
	Type    string `json:"type"`
	Version string `json:"version"`
}

// TypeSetModel labels a client set_model event.
const TypeSetModel = "set_model"

// SetModel overrides the model a session uses for subsequent turns.
type SetModel struct {
	Type    string `json:"type"`
	Session string `json:"session"`
	Model   string `json:"model"`
}

// decoder decodes one raw line into a typed event.
type decoder func([]byte) (any, error)

// decoders maps every known event type to its typed decoder. Unknown types
// fall through to the Envelope (forward compatibility per PROTOCOL.md).
var decoders = map[string]decoder{
	TypeUserMessage:   typed[UserMessage],
	TypeSetModel:      typed[SetModel],
	TypeInterrupt:     typed[Interrupt],
	TypeCreateSession: typed[CreateSession],
	TypeListSessions:  typed[ListSessions],
	TypeLoadSession:   typed[LoadSession],
	TypeSessionMeta:   typed[SessionMeta],
	TypeHistory:       typed[History],
	TypeSessionList:   typed[SessionList],
	TypeTurnStart:     typed[TurnStart],
	TypeTextDelta:     typed[TextDelta],
	TypeToolCallStart: typed[ToolCallStart],
	TypeToolCallEnd:   typed[ToolCallEnd],
	TypeError:         typed[ErrorEvent],
	TypeTurnEnd:       typed[TurnEnd],
	TypeSessionTitle:  typed[SessionTitle],
	TypeServerHello:   typed[ServerHello],
	TypeRenameSession: typed[RenameSession],
	TypeVersionQuery:  typed[VersionQuery],
}

// Decode classifies one raw NDJSON line into a typed event. It returns the
// Envelope for unknown types and an error only for malformed JSON.
func Decode(line []byte) (any, error) {
	var env Envelope
	if err := json.Unmarshal(line, &env); err != nil {
		return nil, fmt.Errorf("decoding envelope: %w", err)
	}

	if dec, ok := decoders[env.Type]; ok {
		return dec(line)
	}

	return env, nil
}

// typed unmarshals a line into T.
func typed[T any](line []byte) (any, error) {
	var ev T
	if err := json.Unmarshal(line, &ev); err != nil {
		return nil, fmt.Errorf("decoding event: %w", err)
	}

	return ev, nil
}
