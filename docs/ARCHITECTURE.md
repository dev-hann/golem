# golem Architecture

Last updated: 2026-10-12. If code and this document disagree, the code is wrong — fix the code or update this document in the same change.

Implementation status lives in [ROADMAP.md](ROADMAP.md): phases 1–3 are implemented; sections describing later phases are specification the implementation must grow toward.

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

| Path | Status | Responsibility | May import |
|---|---|---|---|
| `cmd/golem` | implemented | Main binary. Bare `golem` (or `golem tui`) ensures a server is running — spawning a detached `golem serve` when the socket is dead — then runs the TUI. Also `serve`, `chat`, `ask`. Wiring only — no business logic. | `internal/*`, `pkg/*` |
| `cmd/golem-tui` | implemented | TUI binary. Thin `main` wiring; the app lives in `internal/tui`. | `internal/cli`, `internal/tui` |
| `internal/tui` | implemented | The Bubble Tea app: streaming chat view, collapsible tool blocks, session picker, interrupt, agent status line (thinking/responding/tool + elapsed, model and last-turn usage while idle — derived from existing protocol events, no wire changes). Sessions are created lazily — the first submitted message triggers `create_session` (ADR-0018). All rendering goes through `internal/tui/kit`. | `internal/tui/kit`, `pkg/golemclient` |
| `internal/tui/kit` | implemented | The TUI design system: semantic color tokens, pure-data themes (default: Tokyo Night; `ThemeFromMap` is the future config hook), derived styles, shared components (transcript lines, status bar, picker). Raw colors are forbidden outside this package. | stdlib, `lipgloss`, `bubbletea` |
| `internal/cli` | implemented | Subcommand implementations (`ask`, `serve`, `chat`) and the bare-`golem` launcher (ensure-server + TUI): provider wiring, default socket/session paths, the line REPL (lazy session create — ADR-0018), socket probing and detached server spawn. One layer below `cmd` so it is testable without spawning processes. | `internal/agent`, `internal/llm`, `internal/server`, `internal/session`, `internal/tools`, `internal/tui`, `pkg/golemclient` |
| `internal/llm` | partial | `Provider` interface + adapters. `anthropic` (reference) and `openai` — one adapter for every OpenAI-compatible endpoint: openai, zai, ollama, gateways (ADR-0014). Streaming normalization. | stdlib, provider SDKs |
| `internal/agent` | implemented | Agent loop: send context → receive tool calls → dispatch tools → append results → repeat until turn end. | `internal/llm`, `internal/tools`, `internal/session` |
| `internal/tools` | implemented | Built-in tools: read, write, edit, bash, grep, glob. Pure execution, no LLM knowledge. | stdlib |
| `internal/server` | implemented | Transport listeners (unix socket, TCP, WS later), connection handling, event routing. | `internal/agent`, `internal/session`, `pkg/golemclient` |
| `internal/session` | implemented | Append-only JSONL session store + in-memory cache with bounded size. Replay on load. | stdlib |
| `internal/mcp` | planned (phase 6) | MCP client: process lifecycle, JSON-RPC over stdio, tool discovery. | stdlib (or MCP Go SDK — ADR required to adopt) |
| `internal/config` | partial | Layered TOML config load & merge. Bootstrapped: defaults, global file (`[provider]` sections), `GOLEM_PROVIDER`/`GOLEM_MODEL` env overrides, `ref:` secret resolution. Remaining sections and precedence layers land in phase 4–5. | stdlib, `BurntSushi/toml` |
| `pkg/golemclient` | implemented | **Public** client library. Protocol types + a typed client over any transport. Future apps build on this. | stdlib only — never `internal/*` |
| `apps/` | future | Web/desktop clients. Not part of the Go module build. | — |

**Dependency direction:** `cmd → internal → pkg` is wrong; the rule is: `cmd` may import everything; `internal` packages import other `internal` packages and `pkg`; `pkg` imports stdlib/external only. Layer flow inside `internal`: `server → agent → (llm, tools) → session`. Never upward.

## The agent loop

One **turn** = one user message → zero or more tool rounds → final assistant message. Rounds are unbounded (ADR-0017): the loop ends when the model stops calling tools or the context is cancelled.

```
for {
    stream := provider.Stream(ctx, systemPrompt, history, toolSpecs)
    for event := range stream {
        case TextDelta:   emit text_delta; accumulate
        case ToolCall:    emit tool_call_start
    }
    if no tool calls: break
    for each tool call:
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
| Ollama | `openai-go` + `WithBaseURL` against `:11434/v1` (ADR-0014) | Local models; no API key |

Status: `anthropic` and one `openai`-compat adapter are implemented (ADR-0014 — zai and ollama ride the compat adapter via `base_url`). Provider selection is config-driven (`provider.default`, `GOLEM_PROVIDER`, single-key inference when no config exists); models resolve as `GOLEM_MODEL` > config > preset default. The `/model` runtime switch lands with the rest of phase 5.

## Autonomous tool execution

Tools execute without an interactive approval gate (ADR-0016). Observability, not gating, is the contract:

- Every invocation is visible as `tool_call_start` (name + arguments) followed by `tool_call_end` (ok + result) on the wire and in the session transcript.
- Execution safety rails remain: per-command wall-clock timeouts (`[tools].bash_timeout_secs`) and per-result byte budgets (`[tools].output_budget_bytes`).
- The removed gate (prefix whitelist, prompt/all_auto modes, `approval_request`/`approval_response`) is documented in ADR-0010's supersession note.

## Plugins (MCP)

External tool plugins are MCP servers declared in config (**planned, roadmap phase 6** — `internal/mcp` is not implemented yet). golem manages their lifecycle (declared → starting → ready → running → failed/restarting → stopped) and merges their tools into the agent's tool registry with an `mcp_<server>_<tool>` naming scheme. See [CONFIG.md](CONFIG.md) for the state machine and configuration.

## Memory budget

Design target ≤ 150 MB RSS under normal use, enforced by `GOMEMLIMIT` (config `server.mem_limit_mb`):

- No local tokenizer — token counts come from provider usage fields.
- Grep/glob tool output is truncated to a byte budget before entering context.
- Session cache is bounded; JSONL files stream from disk.

## Allowed dependencies

stdlib, plus: `anthropics/anthropic-sdk-go`, `openai/openai-go`, `ollama/ollama/api`, `BurntSushi/toml`, `charmbracelet/bubbletea` (+`lipgloss`, `bubbles`, `x/ansi` — the ANSI-aware overlay merge in `internal/tui/kit`), `oklog/ulid`, `golang.org/x/net/html` (HTML→text in `internal/tools/webfetch.go` — ADR-0020). Anything else requires an ADR.

## Non-goals (for v1)

- LSP integration, sub-agents, context compaction — revisit after roadmap phase 5 (see [ROADMAP.md](ROADMAP.md)).
- Windows named pipes (unix sockets + TCP only initially).
- Multi-user / remote auth on the server. Local, single-user by design.
