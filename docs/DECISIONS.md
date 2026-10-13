# Architecture Decision Records

Chronological. Each ADR is immutable once accepted; superseding an ADR adds a new one referencing the old. New architectural decisions require a new ADR here, in the same change that implements them.

---

## ADR-0001: Build a new agent instead of contributing to opencode/jcode

- **Status:** Accepted (2026-09-26)
- **Context:** opencode is memory-heavy (Bun/TS runtime, ~500 MB+ RSS observed); jcode has sluggish state management. Both are established tools with large codebases whose core tradeoffs oppose the goals here.
- **Decision:** Build golem — a Go coding agent optimized for low memory and fast state handling.
- **Consequences:** We own maintenance (estimated 2–4 h/month steady-state, spiking when providers change models). Scope stays deliberately small; every feature must justify its maintenance cost.

## ADR-0002: Go, not Rust

- **Status:** Accepted (2026-09-26)
- **Context:** Rust would minimize memory further (~30–60 MB vs ~100–150 MB). But a coding agent is JSON-glue + network waiting; the bottleneck is LLM latency, not local compute. Rust's ownership/borrow machinery adds 1.5–3× development cost for dynamic JSON manipulation, which dominates this codebase.
- **Decision:** Go 1.25+.
- **Consequences:** GC pauses are irrelevant at this scale; `GOMEMLIMIT` caps RSS. Memory goal (≤150 MB) met with margin vs the 500 MB+ baseline of existing tools.

## ADR-0003: Client/server split — server owns all state

- **Status:** Accepted (2026-09-26)
- **Context:** A monolithic TUI dies with the terminal; long agent turns should survive client restarts. Future web/desktop clients need a stable surface.
- **Decision:** Long-lived agent server process; clients connect over a socket and render events. Sessions persist server-side.
- **Consequences:** Protocol becomes a product surface (see PROTOCOL.md). One extra process (~30–80 MB), acceptable within budget.

## ADR-0004: NDJSON codec over unix socket; transports are pluggable

- **Status:** Accepted (2026-09-26)
- **Alternatives considered:** HTTP+SSE (more moving parts, no benefit locally), gRPC (proto toolchain + codegen overhead unjustified for one local binary pair), WebSocket-only (needed later for web, not for local IPC).
- **Decision:** Line-delimited JSON in both directions. Default transport: unix domain socket. TCP and WebSocket listeners reuse the identical codec.
- **Consequences:** Debuggable with `curl`/`nc`; zero framework dependencies in the server; adding web support later requires a transport shim only, not a protocol change.

## ADR-0005: Official provider SDKs behind one thin interface; Z.ai via OpenAI-compatible endpoint

- **Status:** Accepted (2026-09-27)
- **Context:** Four providers needed: Anthropic, OpenAI, Z.ai (GLM), Ollama. Multi-provider frameworks (langchaingo, eino) add heavy abstraction contradicting the lightweight goal. Z.ai ships no official Go SDK, but its API is OpenAI-compatible (and Anthropic-compatible).
- **Decision:** `Provider` interface in `internal/llm`; adapters: `anthropics/anthropic-sdk-go`, `openai/openai-go`, `ollama/ollama/api`, and Z.ai as `openai-go` with `WithBaseURL` override.
- **Consequences:** Provider API changes surface in adapters only. New providers ≈ 100-line adapter + config entry.

## ADR-0006: Bubble Tea for the TUI

- **Status:** Accepted (2026-09-26)
- **Context:** Candidates: Bubble Tea (Elm-architecture, composable, de facto standard), tview (widget-rich, weaker state model), gocui (low-level, maintenance mode).
- **Decision:** `charmbracelet/bubbletea` + `lipgloss` + `bubbles`.
- **Consequences:** State management is explicit (Model/Update/View), which suits AI-driven development and testing.

## ADR-0007: MCP as the plugin system; reject Go `plugin` and custom plugin formats

- **Status:** Accepted (2026-09-27)
- **Context:** Extensibility is required (extra tools). Options: invent a plugin ABI (cost, no ecosystem), Go `plugin` .so (Linux-only, fragile version coupling), MCP (open standard, JSON-RPC over stdio, large existing server ecosystem).
- **Decision:** External tool plugins are MCP servers declared in config; golem ships an MCP client managing their lifecycle. Hooks (pre/post tool) are deferred. Internal providers/tools stay compile-time Go.
- **Consequences:** Plugin = separate process (isolation, any language). MCP client is ~200 lines if hand-rolled; adopting `modelcontextprotocol/go-sdk` instead requires an ADR.

## ADR-0008: Append-only JSONL session store; no database

- **Status:** Accepted (2026-09-27)
- **Context:** Sessions need crash-safe persistence, trivial inspection, and fast load — not queries. SQLite et al. add binary formats, migrations, and a dependency for zero benefit at single-user scale.
- **Decision:** One JSONL file per session under `~/.local/share/golem/sessions/`; events are appended before emission to clients; replay folds events into state.
- **Consequences:** The wire protocol and storage format are the same objects (see PROTOCOL.md). Bounded in-memory cache; cold sessions stream from disk.

## ADR-0009: Monorepo, single Go module; `pkg/golemclient` as the public client contract

- **Status:** Accepted (2026-09-27)
- **Context:** Future web/desktop clients must share the protocol without forking types. Multi-module workspaces add ceremony without need.
- **Decision:** One `go.mod`. Protocol types live in `pkg/golemclient` (importable by all clients). Future non-Go clients live under `apps/` outside the module build.
- **Consequences:** One version set; protocol changes are visible as compile errors in `golemclient`.

## ADR-0010: Approval gate with read-only whitelist

- **Status:** Accepted (2026-09-27)
- **Context:** Unattended shell execution is the primary risk of a coding agent.
- **Decision:** All shell-side-effect tools route through `internal/approval`. Configurable `auto_prefixes` (ls, cat, git status, grep, rg, …) run unprompted; everything else blocks on an `approval_request` until answered or timed out (120 s → deny). `all_auto` exists as a config-only escape hatch with documentation warnings.
- **Consequences:** One round-trip per sensitive command; acceptable.

## ADR-0011: Apache-2.0 license

- **Status:** Accepted (2026-09-27)
- **Context:** Project will be open-sourced.
- **Decision:** Apache-2.0 (explicit patent grant, NOTICE discipline) over MIT.
- **Consequences:** None technical.

## ADR-0012: Layered TOML configuration

- **Status:** Accepted (2026-09-27)
- **Context:** Need global + per-project + env overrides; JSON lacks comments; YAML complexity unneeded.
- **Decision:** `BurntSushi/toml`. Precedence (low→high): built-in defaults → `~/.config/golem/config.toml` → `<project>/.golem.toml` → env (`GOLEM_*`) → CLI flags. Maps deep-merge; slices replace. Secrets use `ref:ENV_NAME` indirection or env directly — plaintext keys in files are warned against.
- **Consequences:** One parser dependency; schema documented in CONFIG.md.

## ADR-0013: Strict uniformity gates (lint, coverage, docs-as-code)

- **Status:** Accepted (2026-09-27)
- **Context:** Development is AI-driven across sessions; code uniformity and stable context are requirements, not preferences.
- **Decision:** golangci-lint with maximum practical strictness (gofumpt, revive strict, exhaustive, gosec, complexity gates, 120 cols); race-detector tests; 80% coverage floor in CI; docs under `docs/` are the source of truth and must change with behavior (enforced socially via AGENTS.md review gate).
- **Consequences:** Occasional lint friction is accepted as the price of uniformity.

## ADR-0014: One openai-compat adapter for openai, zai, ollama, and gateways

- **Status:** Accepted (2026-10-13)
- **Context:** ADR-0005 planned four adapters, including `ollama/ollama/api` for local models. In practice every planned non-Anthropic provider speaks the OpenAI chat-completions wire format: zai and gateways via `base_url`, ollama via its `:11434/v1` compatibility endpoint. Four adapters would normalize the same protocol four times.
- **Decision:** `internal/llm/openai` is the single OpenAI-compatible adapter (`openai/openai-go` + `WithBaseURL`). Provider ids in `internal/llm` presets select kind, endpoint, key env, and default model: `anthropic`, `openai`, `zai`, `ollama`. The planned `ollama/ollama/api` dependency is dropped. This amends ADR-0005; no other framework/SDK adoption (jetify-com/ai, GoAI, langchaingo, eino were evaluated and rejected — alpha-stage or heavyweight).
- **Consequences:** One streaming/tool-call normalization to test; new OpenAI-compatible providers are a preset entry plus optional config. Native ollama features (keep-alive hints, model pulls) stay out of scope until needed.
