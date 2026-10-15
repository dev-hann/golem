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

Session management. `create_session` → server emits `session_meta`. `list_sessions` → server emits `session_list`. `load_session` → server emits `session_meta` followed by a `history` replay batch for that client. `rename_session` → server sanitizes the title, persists the newest `session_title` record, and confirms with a `session_title` event. `delete_session` → the server cancels a running turn in that session (opencode-style), waits for it to drain, removes the session's file, and confirms with `session_deleted`; a deleted session refuses appends permanently — late writers get an error, never a resurrected file. `compact_session` → server summarizes the folded history (bounded 60s side call, 16 KiB summary cap — ADR-0022), appends a `session_compacted` barrier record, and confirms with a `session_compacted` event; on failure it emits `compact_failed` and the history is untouched. `version_query` → server emits `server_hello` (build handshake for launcher freshness checks).

Clients create their session lazily (ADR-0018): no session is minted until the first submitted message — an abandoned launch must leave nothing behind. A client may connect, `list_sessions`, and `load_session` without ever sending `create_session`.

```json
{"type":"create_session","cwd":"/home/user/project"}
{"type":"list_sessions"}
{"type":"delete_session","session":"01J..."}
{"type":"load_session","session":"01J..."}
```

| Field | Type | Description |
|---|---|---|
| `cwd` | string | Working directory pinned for the session (create only) |

## Server → Client

### `session_meta`

```json
{"type":"session_meta","session":"01J...","cwd":"/home/user/project","created":"2026-09-27T10:00:00Z","turns":12,"title":"Fix auth middleware","busy":true}
```

`busy` is live server state at load time: true when a turn is running in that session, omitted otherwise (create, idle load). Like `session_list`'s busy it is a snapshot, not a subscription — the ongoing `turn` events stream continues on the connection. Its purpose: a client that loads a busy session knows the replay ends mid-turn and must render the running state, not idle.

`title` is the session's folded title (rename or generation) at create/load time, omitted while untitled — clients render a placeholder until a `session_title` event or their next `session_list` arrives.

### `history`

Replay batch after `load_session`. Contains zero or more compacted prior events so a reconnecting client can render the transcript.

```json
{"type":"history","session":"01J...","events":[ ...compact event objects... ]}
```

### `session_list`

```json
{"type":"session_list","sessions":[{"id":"01J...","title":"Fix auth middleware","cwd":"/p","created":"2026-09-27T10:00:00Z","turns":12,"last_active":"2026-09-27T12:30:00Z","busy":false}]}
```

`title` is optional: the server generates it with a model side call after a session's turn ends while the session is still untitled (persisted as a `session_title` record) and omits it while absent or failed. A failed generation is retried on the next turn's completion until one lands. `busy` is live server state: true while a turn is running in that session, false on idle — a snapshot at list time, not a subscription.

### `session_title`

Server-persisted record naming a session for listings. Emitted to a client after `rename_session` confirms; clients encounter stored titles through `session_list`.

```json
{"type":"session_title","session":"01J...","title":"Fix auth middleware"}
```

### `session_deleted`

Confirmation that a `delete_session` completed: the session's stored file is gone. Emitted to the requesting client only; other clients reconcile on their next `list_sessions`. Events still in flight for the deleted session (a cancelled turn's error/turn_end tail) carry no forward meaning — clients drop them. A client whose *current* session was deleted falls back to the lazy-start state: no session until the next `create_session`.

```json
{"type":"session_deleted","session":"01J..."}
```
### `session_compacted` / `compact_failed`

Manual compaction (ADR-0022). `compact_session` asks the server to fold the session's history into a summary; the server runs a bounded provider side call, appends a `session_compacted` barrier record (append-only storage — never a rewrite), and confirms:

```json
{"type":"compact_session","session":"01J..."}
{"type":"session_compacted","session":"01J...","summary":"Built the auth middleware; edit.go remains..."}
{"type":"compact_failed","session":"01J...","reason":"summary call failed: 429 rate limited"}
```

The barrier is a fold rule, not just a receipt: everything before it drops from the model's context, replaced by one synthesized user message carrying the summary. Title, turn count, and cwd survive. Replaying a session (`history`) includes the barrier — clients apply the same rule and render the summary block, not the dropped turns. Refused requests (turn running, empty session) answer `error` with `invalid_request`.
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
| `thinking_delta` | — | Model is reasoning (extended thinking — Anthropic native, GLM's `reasoning_content` normalized by the adapter). No content on the wire — the event exists so clients can render a reasoning state; reasoning text is never streamed, stored, or replayed |
| `text_delta` | `delta` | Assistant text chunk (append) |
| `retry` | `attempt`, `retries`, `delay_ms` | Server is about to retry the provider (`attempt` of at most `retries`, after sleeping `delay_ms`). Emitted before the wait so clients can render a retrying state instead of a frozen one |
| `tool_call_start` | `call_id`, `tool`, `args` | Tool invocation; `args` is the parsed JSON object |
| `tool_call_end` | `call_id`, `ok`, `result` | Tool finished. Failures carry the error text in `result`; `result` truncated to 16 KiB display budget |
| `error` | `code`, `message` | Recoverable error within the turn (tool failures already reported via `tool_call_end`; this is loop/provider level) |
| `turn_end` | `usage` | Turn complete. `usage`: `{"input_tokens":n,"output_tokens":n}` |

Examples:

```json
{"type":"turn_start","session":"01J...","turn":"01J...","provider":"anthropic","model":"claude-opus-5"}
{"type":"thinking_delta","session":"01J...","turn":"01J..."}
{"type":"text_delta","session":"01J...","turn":"01J...","delta":"Reading the test file.\n"}
{"type":"retry","session":"01J...","turn":"01J...","attempt":2,"retries":6,"delay_ms":4000}
{"type":"tool_call_start","session":"01J...","turn":"01J...","call_id":"tc_1","tool":"read","args":{"path":"internal/tools/edit.go"}}
{"type":"tool_call_end","session":"01J...","turn":"01J...","call_id":"tc_1","ok":true,"result":"package tools..."}
{"type":"turn_end","session":"01J...","turn":"01J...","usage":{"input_tokens":4211,"output_tokens":380}}
```

## Error codes (`error` events)

| Code | Meaning | Client action |
|---|---|---|
| `provider_error` | LLM API call failed (rate limit, auth, 5xx), or the round ended truncated with no visible output (reasoning budget burn — `llm.ErrEmptyReasoningRound`), or produced nothing at all (`agent.ErrEmptyResponse`) | Show message; turn ended |
| `provider_overloaded` | Provider explicitly overloaded | Offer retry |
| `context_length` | Conversation exceeds model window | Offer `/compact` (manual compaction, ADR-0021) or a new session |
| `session_not_found` | Unknown session ID | Refetch `session_list` |
| `invalid_request` | Malformed client event | Log; ignore |
| `internal` | Unexpected server error | Show message |

## Session persistence mirror

Every event in this protocol (except `interrupt` and session management requests) is appended to the session's JSONL file as-is before it is emitted to clients. The JSONL file therefore *is* the transcript and replay source; there is no separate storage format. Sessions recorded before the approval gate's removal may still contain legacy `approval_request` lines; replay skips unknown event types. Deletion (`delete_session`) removes the whole file (ADR-0019); appends to a deleted session fail permanently — the file can never resurrect.

## Versioning

The protocol has no version negotiation in v1. Breaking changes require: new `type` names rather than changed meanings, plus a note here. When a second consumer exists, add a `hello` handshake with `protocol_version`.

> **Breaking change note:** the `approval_request` server event, the `approval_response` client event, and the `approval_timeout` error code were removed when the interactive approval gate was dropped (ADR-0016). Tools now execute autonomously and are observable via `tool_call_start`/`tool_call_end`.
