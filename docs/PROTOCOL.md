# golem Protocol (NDJSON)

Last updated: 2026-09-27. This document is the contract between golem-server and every client (TUI, CLI, future web/desktop). Changing anything here requires updating this file in the same commit.

## Framing

- Transport: unix domain socket (default), TCP, or WebSocket — identical payload either way.
- Encoding: UTF-8 JSON, **one message per line**, terminated by `\n` (NDJSON). No blank lines. No length prefixes.
- Both directions are asynchronous event streams. Correlate via `session` and `turn` IDs; clients must tolerate interleaved events from different sessions on a single connection.
- Unknown event types must be ignored (forward compatibility). Fields not listed for an event type must be ignored.

## Common fields

| Field | Type | Present | Description |
|---|---|---|---|
| `type` | string | always | Event type, snake_case |
| `session` | string | always | Session ULID |
| `turn` | string | most events | Turn ULID; set once a user message starts a turn |

## Client → Server

### `user_message`

Send a user prompt. Starts a new turn in the given session.

```json
{"type":"user_message","session":"01J...","text":"fix the failing test in internal/tools"}
```

| Field | Type | Description |
|---|---|---|
| `text` | string | Non-empty user prompt |

### `interrupt`

Cancel the current turn. Server stops the agent loop at the next await point; partial events already emitted stand.

```json
{"type":"interrupt","session":"01J...","turn":"01J..."}
```

### `set_model`

Override the model this session uses for subsequent turns (same provider; switching providers is a later phase). Applies from the next turn; `turn_start` reports the model actually in effect. Empty models are `invalid_request`; unknown sessions get `session_not_found`. Not persisted — replay restores the transcript, not the override.

```json
{"type":"set_model","session":"01J...","model":"glm-5.3-flash"}
```

### `create_session` / `list_sessions` / `load_session`

Session management. `create_session` → server emits `session_meta`. `list_sessions` → server emits `session_list`. `load_session` → server emits `session_meta` followed by a `history` replay batch for that client. `rename_session` → server sanitizes the title, persists the newest `session_title` record, and confirms with a `session_title` event. `version_query` → server emits `server_hello` (build handshake for launcher freshness checks).

```json
{"type":"create_session","cwd":"/home/user/project"}
{"type":"list_sessions"}
{"type":"load_session","session":"01J..."}
```

| Field | Type | Description |
|---|---|---|
| `cwd` | string | Working directory pinned for the session (create only) |

## Server → Client

### `session_meta`

```json
{"type":"session_meta","session":"01J...","cwd":"/home/user/project","created":"2026-09-27T10:00:00Z","turns":12}
```

### `history`

Replay batch after `load_session`. Contains zero or more compacted prior events so a reconnecting client can render the transcript.

```json
{"type":"history","session":"01J...","events":[ ...compact event objects... ]}
```

### `session_list`

```json
{"type":"session_list","sessions":[{"id":"01J...","title":"Fix auth middleware","cwd":"/p","created":"2026-09-27T10:00:00Z","turns":12,"last_active":"2026-09-27T12:30:00Z"}]}
```

`title` is optional: the server generates it with a model side call after a session's first turn (persisted as a `session_title` record) and omits it while absent or failed.

### `session_title`

Server-persisted record naming a session for listings. Emitted to a client after `rename_session` confirms; clients encounter stored titles through `session_list`.

```json
{"type":"session_title","session":"01J...","title":"Fix auth middleware"}
```

### Version handshake

`version_query` asks the running server to identify its build; `server_hello` answers. The launcher uses this to detect (and restart) a stale detached server — a server that cannot answer is treated as outdated.

```json
{"type":"version_query"}
{"type":"server_hello","version":"v0.1.9"}
```

### `turn` events

Emitted in order during a turn. All carry `turn`.

| Type | Fields | Meaning |
|---|---|---|
| `turn_start` | `provider`, `model` | Turn began |
| `text_delta` | `delta` | Assistant text chunk (append) |
| `tool_call_start` | `call_id`, `tool`, `args` | Tool invocation; `args` is the parsed JSON object |
| `tool_call_end` | `call_id`, `ok`, `result` | Tool finished. Failures carry the error text in `result`; `result` truncated to 16 KiB display budget |
| `error` | `code`, `message` | Recoverable error within the turn (tool failures already reported via `tool_call_end`; this is loop/provider level) |
| `turn_end` | `usage` | Turn complete. `usage`: `{"input_tokens":n,"output_tokens":n}` |

Examples:

```json
{"type":"turn_start","session":"01J...","turn":"01J...","provider":"anthropic","model":"claude-opus-5"}
{"type":"text_delta","session":"01J...","turn":"01J...","delta":"Reading the test file.\n"}
{"type":"tool_call_start","session":"01J...","turn":"01J...","call_id":"tc_1","tool":"read","args":{"path":"internal/tools/edit.go"}}
{"type":"tool_call_end","session":"01J...","turn":"01J...","call_id":"tc_1","ok":true,"result":"package tools..."}
{"type":"turn_end","session":"01J...","turn":"01J...","usage":{"input_tokens":4211,"output_tokens":380}}
```

## Error codes (`error` events)

| Code | Meaning | Client action |
|---|---|---|
| `provider_error` | LLM API call failed (rate limit, auth, 5xx) | Show message; turn ended |
| `provider_overloaded` | Provider explicitly overloaded | Offer retry |
| `context_length` | Conversation exceeds model window | Suggest new session (compaction is a non-goal for v1) |
| `session_not_found` | Unknown session ID | Refetch `session_list` |
| `invalid_request` | Malformed client event | Log; ignore |
| `internal` | Unexpected server error | Show message |

## Session persistence mirror

Every event in this protocol (except `interrupt` and session management requests) is appended to the session's JSONL file as-is before it is emitted to clients. The JSONL file therefore *is* the transcript and replay source; there is no separate storage format. Sessions recorded before the approval gate's removal may still contain legacy `approval_request` lines; replay skips unknown event types.

## Versioning

The protocol has no version negotiation in v1. Breaking changes require: new `type` names rather than changed meanings, plus a note here. When a second consumer exists, add a `hello` handshake with `protocol_version`.

> **Breaking change note:** the `approval_request` server event, the `approval_response` client event, and the `approval_timeout` error code were removed when the interactive approval gate was dropped (ADR-0016). Tools now execute autonomously and are observable via `tool_call_start`/`tool_call_end`.
