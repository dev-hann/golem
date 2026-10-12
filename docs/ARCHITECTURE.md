# golem Architecture

Last updated: 2026-09-27. If code and this document disagree, the code is wrong — fix the code or update this document in the same change.

## Overview

golem is a coding agent split into a long-lived **agent server** and thin **clients**. The server owns all state (sessions, tool execution, LLM streaming, plugin lifecycle). Clients are replaceable surfaces — TUI today; web and desktop later — speaking one NDJSON protocol.

```
┌──────────┐  ┌───────────┐  ┌─────────┐  ┌─────┐
│ TUI      │  │ Web (later)│  │ Desktop │  │ CLI │
│ BubbleTea│  │ via WS     │  │ Wails等 │  │     │
└────┬─────┘  └─────┬─────┘  └────┬────┘  └──┬──┘
     │ unix socket  │ TCP/WebSocket │ TCP     │ unix socket
     └──────────────┴───────┬───────┴─────────┘
                     NDJSON protocol (shared codec)
                             │
                  ┌──────────▼──────────┐
                  │  golem-server       │
                  │  transport (unix/tcp│
                  │   /ws listeners)    │
                  │  session manager    │
                  │  agent loop         │
                  │  approval gate      │
                  │  tool registry      │
                  │  provider interface │
                  │  MCP client         │
                  └───┬─────┬─────┬────┘
              ┌───────▼─┐ ┌─▼─────┐ └───┬─────────┐
              │Anthropic│ │OpenAI │  Ollama   Z.ai │
              │  SDK    │ │ SDK   │  (api pkg) (OpenAI-compat)│
              └─────────┘ └───────┘ └───────────┘
```

## Modules

| Path | Responsibility | May import |
|---|---|---|
| `cmd/golem` | Server binary. Subcommands: `serve`, `chat`, `ask`. Wiring only — no business logic. | `internal/*`, `pkg/*` |
| `cmd/golem-tui` | Bubble Tea client binary. Rendering and input only. | `pkg/golemclient`, `internal/*` for shared UI types only |
| `internal/llm` | `Provider` interface + adapters (anthropic, openai, zai, ollama). Streaming normalization. | stdlib, provider SDKs |
| `internal/agent` | Agent loop: send context → receive tool calls → dispatch tools → append results → repeat until turn end. | `internal/llm`, `internal/tools`, `internal/session`, `internal/approval` |
| `internal/tools` | Built-in tools: read, write, edit, bash, grep, glob. Pure execution, no LLM knowledge. | stdlib |
| `internal/server` | Transport listeners (unix socket, TCP, WS later), connection handling, event routing. | `internal/agent`, `internal/session`, `pkg/golemclient` |
| `internal/session` | Append-only JSONL session store + in-memory cache with bounded size. Replay on load. | stdlib |
| `internal/approval` | Approval policy engine: whitelist prefixes, prompt/auto modes, pending-approval state. | stdlib |
| `internal/mcp` | MCP client: process lifecycle, JSON-RPC over stdio, tool discovery. | stdlib (or MCP Go SDK — ADR required to adopt) |
| `internal/config` | Layered TOML config load & merge. | stdlib, `BurntSushi/toml` |
| `pkg/golemclient` | **Public** client library. Protocol types + a typed client over any transport. Future apps build on this. | stdlib only — never `internal/*` |
| `apps/` | Future web/desktop clients. Not part of the Go module build. | — |

**Dependency direction:** `cmd → internal → pkg` is wrong; the rule is: `cmd` may import everything; `internal` packages import other `internal` packages and `pkg`; `pkg` imports stdlib/external only. Layer flow inside `internal`: `server → agent → (llm, tools, approval) → session`. Never upward.

## The agent loop

One **turn** = one user message → zero or more tool rounds → final assistant message.

```
for {
    stream := provider.Stream(ctx, systemPrompt, history, toolSpecs)
    for event := range stream {
        case TextDelta:   emit text_delta; accumulate
        case ToolCall:    emit tool_call_start
    }
    if no tool calls: break
    for each tool call:
        if bash-like && policy requires: emit approval_request, await response
        result := registry.Execute(ctx, name, args)
        emit tool_call_end; append result to history
    }
}
emit done; persist turn to session JSONL
```

Hard rules:

- The loop is the **only** place that talks to both providers and tools. Tools never call the LLM; providers never see tool implementations (only their JSON Schemas).
- Every turn is persisted as events before `done` is emitted (crash-safe replay).
- The loop is cancellable at every await point (context).

## Session model

- Storage: `~/.local/share/golem/sessions/<id>.jsonl` — one JSON event per line, append-only.
- Replay = read file, fold events into conversation state. No second format, no migration.
- In-memory cache holds the N most recently active sessions (config `server.session_cache`); others load on demand.
- Session IDs: ULIDs (sortable, no coordination).

## Transport & protocol

See [PROTOCOL.md](PROTOCOL.md) for the full event catalog. Summary:

- **Codec:** NDJSON — one JSON object per `\n`-terminated line, UTF-8, in both directions. Transport-agnostic by design.
- **Default transport:** unix domain socket at `$XDG_RUNTIME_DIR/golem.sock` (fallback `~/.local/share/golem/golem.sock`).
- TCP listener optional (config `server.tcp_addr`) for desktop/web clients later; WebSocket transport wraps the same codec. No protocol changes when transports are added.

## Providers

One interface (in `internal/llm`):

```go
type Provider interface {
    Stream(ctx context.Context, req Request) (<-chan Event, error)
}
```

Adapters normalize each SDK's stream into the common event channel (text deltas, tool calls, usage, errors). Provider choice is config-driven; no provider is special-cased in the agent loop.

| Provider | Adapter basis | Notes |
|---|---|---|
| Anthropic | `anthropics/anthropic-sdk-go` | Reference adapter, built first |
| OpenAI | `openai/openai-go` | Responses API with tool calling |
| Z.ai (GLM) | `openai-go` + `WithBaseURL` against OpenAI-compatible endpoint | No official Go SDK exists |
| Ollama | `ollama/ollama/api` | Local models |

## Approval gate

All shell-executing tools (built-in `bash`, MCP tools that declare side effects) pass through `internal/approval` before execution:

- `auto_prefixes` (config): read-only commands (ls, cat, git status, grep, rg, …) run without prompting.
- Everything else: agent loop emits `approval_request`, blocks until `approval_response`, with timeout.
- Policy modes: `prompt` (default), `all_auto` (danger, config-only). Documented in CONFIG.md.

## Plugins (MCP)

External tool plugins are MCP servers declared in config. golem manages their lifecycle (declared → starting → ready → running → failed/restarting → stopped) and merges their tools into the agent's tool registry with an `mcp_<server>_<tool>` naming scheme. See [CONFIG.md](CONFIG.md) for the state machine and configuration.

## Memory budget

Design target ≤ 150 MB RSS under normal use, enforced by `GOMEMLIMIT` (config `server.mem_limit_mb`):

- No local tokenizer — token counts come from provider usage fields.
- Grep/glob tool output is truncated to a byte budget before entering context.
- Session cache is bounded; JSONL files stream from disk.

## Allowed dependencies

stdlib, plus: `anthropics/anthropic-sdk-go`, `openai/openai-go`, `ollama/ollama/api`, `BurntSushi/toml`, `charmbracelet/bubbletea` (+`lipgloss`, `bubbles`), `oklog/ulid`. Anything else requires an ADR.

## Non-goals (for v1)

- LSP integration, sub-agents, context compaction — revisit after roadmap phase 5 (see [ROADMAP.md](ROADMAP.md)).
- Windows named pipes (unix sockets + TCP only initially).
- Multi-user / remote auth on the server. Local, single-user by design.
