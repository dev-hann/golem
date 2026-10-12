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

### `approval_response`

Respond to a pending `approval_request` (same `turn`).

```json
{"type":"approval_response","session":"01J...","turn":"01J...","approval_id":"ap_9f2","decision":"allow"}
```

| Field | Type | Description |
|---|---|---|
| `approval_id` | string | ID from `approval_request` |
| `decision` | `"allow" \| "deny"` | `deny` sends the tool a "user denied" error result so the loop continues |

### `interrupt`

Cancel the current turn. Server stops the agent loop at the next await point; partial events already emitted stand.

```json
{"type":"interrupt","session":"01J...","turn":"01J..."}
```

### `create_session` / `list_sessions` / `load_session`

Session management. `create_session` → server emits `session_meta`. `list_sessions` → server emits `session_list`. `load_session` → server emits `session_meta` followed by a `history` replay batch for that client.

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
{"type":"session_list","sessions":[{"id":"01J...","cwd":"/p","created":"2026-09-27T10:00:00Z","turns":12,"last_active":"2026-09-27T12:30:00Z"}]}
```

### `turn` events

Emitted in order during a turn. All carry `turn`.

| Type | Fields | Meaning |
|---|---|---|
| `turn_start` | `provider`, `model` | Turn began |
| `text_delta` | `delta` | Assistant text chunk (append) |
| `tool_call_start` | `call_id`, `tool`, `args` | Tool invocation; `args` is the parsed JSON object |
| `tool_call_end` | `call_id`, `ok`, `result` | Tool finished. Failures carry the error text in `result`; `result` truncated to 16 KiB display budget |
| `approval_request` | `approval_id`, `call_id`, `tool`, `preview` | Loop blocked; awaiting `approval_response`. `preview`: command line + cwd |
| `error` | `code`, `message` | Recoverable error within the turn (tool failures already reported via `tool_call_end`; this is loop/provider level) |
| `turn_end` | `usage` | Turn complete. `usage`: `{"input_tokens":n,"output_tokens":n}` |

Examples:

```json
{"type":"turn_start","session":"01J...","turn":"01J...","provider":"anthropic","model":"claude-opus-5"}
{"type":"text_delta","session":"01J...","turn":"01J...","delta":"Reading the test file.\n"}
{"type":"tool_call_start","session":"01J...","turn":"01J...","call_id":"tc_1","tool":"read","args":{"path":"internal/tools/edit.go"}}
{"type":"tool_call_end","session":"01J...","turn":"01J...","call_id":"tc_1","ok":true,"result":"package tools..."}
{"type":"approval_request","session":"01J...","turn":"01J...","approval_id":"ap_1","call_id":"tc_2","tool":"bash","preview":"rm -rf /tmp/build && make build (cwd: /home/user/project)"}
{"type":"turn_end","session":"01J...","turn":"01J...","usage":{"input_tokens":4211,"output_tokens":380}}
```

## Error codes (`error` events)

| Code | Meaning | Client action |
|---|---|---|
| `provider_error` | LLM API call failed (rate limit, auth, 5xx) | Show message; turn ended |
| `provider_overloaded` | Provider explicitly overloaded | Offer retry |
| `context_length` | Conversation exceeds model window | Suggest new session (compaction is a non-goal for v1) |
| `approval_timeout` | No approval response within 120s | Tool denied by default |
| `session_not_found` | Unknown session ID | Refetch `session_list` |
| `invalid_request` | Malformed client event | Log; ignore |
| `internal` | Unexpected server error | Show message |

## Session persistence mirror

Every event in this protocol (except `approval_response`, `interrupt`, and session management requests) is appended to the session's JSONL file as-is before it is emitted to clients. The JSONL file therefore *is* the transcript and replay source; there is no separate storage format.

## Versioning

The protocol has no version negotiation in v1. Breaking changes require: new `type` names rather than changed meanings, plus a note here. When a second consumer exists, add a `hello` handshake with `protocol_version`.
