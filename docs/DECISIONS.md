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

- **Status:** Superseded by ADR-0016 (2026-10-14)
- **Context:** Unattended shell execution is the primary risk of a coding agent.
- **Decision:** All shell-side-effect tools route through `internal/approval`. Configurable `auto_prefixes` (ls, cat, git status, grep, rg, …) run unprompted; everything else blocks on an `approval_request` until answered or timed out (120 s → deny). `all_auto` exists as a config-only escape hatch with documentation warnings.
- **Consequences:** One round-trip per sensitive command; acceptable. *(In practice the whitelist matched almost no real model-issued command line — redirections like `2>/dev/null` and chained segments forced prompts — turning the gate into per-call friction; see ADR-0016.)*

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

## ADR-0015: API keys stored in the global config by default

- **Status:** Accepted (2026-10-13)
- **Context:** ADR-0012's secrets stance (env vars / `ref:` indirection only) optimized for servers and CI. golem is a local, single-user tool: the env-var flow required every user to maintain an `export` line in their shell profile, and the daemon inherited only the launching shell's environment — producing invisible startup failures ("server not ready within 5s") and a three-round debugging session. Critically, the env-var flow does not keep keys off disk: the export line lands in `~/.zshrc`, a plaintext file with typically looser permissions than golem's 0600 config.
- **Decision:** The setup wizard writes the validated key directly into `~/.config/golem/config.toml` (`api_key = "sk-..."`, file mode 0600), matching the local-CLI precedent (`gh` hosts.yml, `aws` credentials). `ref:ENV_VAR` indirection and the per-provider env fallback remain supported options. The plaintext warning is removed for the global config; the future project-level `.golem.toml` (committable) will still forbid plaintext keys.
- **Consequences:** Setup-to-first-message works with zero shell integration. Users syncing `~/.config/golem` through dotfiles repos or cloud sync must exclude the file (documented). Credential rotation means editing the config and restarting the server.

## ADR-0016: No approval gate — autonomous tool execution with transcript visibility

- **Status:** Accepted (2026-10-14)
- **Context:** golem is a local, single-user agent on a machine the operator controls. ADR-0010's gate was designed for one prompt per genuinely dangerous command, but real model-issued command lines defeated the whitelist almost every call: `2>/dev/null` and other redirections hit the forced-deny substrings, and chained segments (`cd X && … | head`) required every segment to be whitelisted. A representative session (2026-10-14) prompted 58 times across 67 bash calls — all read-only exploration — and the retry churn contributed to hitting the 50-round turn cap. The gate cost more than the risk it priced.
- **Decision:** Remove the interactive approval gate entirely: the `internal/approval` package, the agent-loop gate, the server's `approval_request`/`approval_response` protocol events (and `approval_timeout` error code), the `[approval]` config section, and the TUI approval modal. Tools execute autonomously. The user-facing contract is observability: every invocation is reported through `tool_call_start`/`tool_call_end` and rendered as a transcript tool block. Execution rails stay: per-command timeouts and output budgets (CONFIG.md `[tools]`).
- **Consequences:** Unattended arbitrary command execution is accepted by design; users who need gating should not run golem with credentials they cannot afford to lose. The wire protocol change is breaking but coordinated (server and clients ship together; pre-1.0 per the changelog header). Old session files containing `approval_request` lines replay harmlessly — unknown event types are skipped. Supersedes ADR-0010.

## ADR-0017: Unbounded tool rounds per turn

- **Status:** Accepted (2026-10-14)
- **Context:** The agent loop capped a turn at 50 provider→tool rounds, killing it with `max tool rounds (50) exceeded` and no summary of work in flight. Session logs (2026-10-14) show the cap hit repeatedly on legitimate long tasks — turns of 53–68 tool calls doing exploration, edits, and `make all` verification — not confused models. Each cut left half-finished work the user had to reconstruct from the transcript. The cap also had no off-switch: `maxRounds` was an unexported constant, and no config key existed.
- **Decision:** Remove the round cap. A turn ends only when the model stops calling tools (a text-only response) or the context is cancelled. Cancellation (TUI interrupt, `Esc`, server shutdown) is the guard against a runaway loop — the same posture opencode takes (no default step limit; user interrupt stops the loop). Compaction-style context management remains out of scope.
- **Consequences:** Long autonomous tasks complete in one turn instead of being chopped at 50 rounds with an error. A genuinely confused model now loops (and burns tokens) until the user interrupts — accepted cost for the local, single-user threat model; the transcript makes the loop visible while it runs. Should a configurable cap return, it should follow opencode's shape: config-driven, and on reaching the limit force one text-only summary round (`toolChoice: none`) instead of erroring out. Test coverage moves from "cap errors the turn" to "the loop runs past the old bound and completes / stops on cancellation".

## ADR-0018: Lazy session creation — a client owns no session until it speaks

- **Status:** Accepted (2026-10-14)
- **Context:** Both clients created a session the moment they connected: the TUI's `connect()` sent `create_session` during `Init()`, and `golem chat` sent it before reading the first input line. Every launch — including one abandoned at the empty composer, or a `chat` that only ever sees `/quit` — minted an empty JSONL session on the server and a phantom row in the session picker (title falling back to the directory name, 0 turns). Session lists accumulated this debris; nothing distinguished "opened the TUI" from "had a conversation".
- **Decision:** Clients create their session lazily, on the first submitted message. The TUI queues the line locally, sends `create_session`, and flushes the queue when `session_meta` arrives (a `set_model` picked before the session existed defers ahead of the flush, preserving wire order); `ctrl+x n` before any session is a no-op — there is nothing to reset. `golem chat` sends `create_session` on the first non-blank, non-`/quit` line and waits for `session_meta` then. The server is unchanged: `create_session` → `session_meta` remains the only entry point to a session.
- **Consequences:** Launching a client and leaving without speaking costs nothing on the server; the session picker lists only sessions that hold a message. A submitted line now waits one round-trip (`create_session` → `session_meta`) before the turn starts — paid once per session, invisible in practice, and shown as a `● creating session` status while pending. `load_session` (picker) is unaffected: resuming a known session needs no create. Protocol is unchanged; the contract documented in PROTOCOL.md gains the client-timing note.

## ADR-0019: Session deletion — cancel, drain, remove the file

- **Status:** Accepted (2026-10-15)
- **Context:** Sessions accumulated with no way out: the picker could list, open, and rename but not delete, so dead sessions lived forever. opencode's shape (`session.remove` after a `turn.cancel`, client-side optimistic row removal) fits golem, but golem's storage differs fundamentally — the append-only JSONL store (ADR-0008) means a turn goroutine (or the 90s title side call) holding an open append path can silently *resurrect* a deleted file via `O_APPEND|O_CREATE`.
- **Decision:** `delete_session` cancels a running turn first, waits for its goroutine to drain (`turnState.done`, bounded by 10s against a wedged provider), marks the session mid-delete (`Store.MarkDeleted` — appends fail with `ErrDeleted` while flagged, including the title side call), removes the JSONL file, and confirms with `session_deleted` to the requester only. The flag clears once the delete resolves (deferred `UnmarkDeleted`), so a failed delete leaves the session usable and a successful one leaves nothing to append to. The TUI deletes optimistically (opencode's pattern): `ctrl+d` arms the row — the title swaps to `ctrl+d again to confirm` in the error token — and the second press removes the row immediately and sends the request; any navigation or typing disarms. Deleting the current session resets the client to the lazy-start state (ADR-0018).
- **Consequences:** ADR-0008's append-only discipline is preserved — deletion is a whole-file removal, not record mutation. The confirmation goes to the requesting connection only (no broadcast in v1); other clients reconcile on their next `list_sessions`, and the deleted session's in-flight tail events are documented as droppable. The cancel-drain wait makes `delete_session` synchronous for busy sessions (worst case the 10s bound). Recovery is trivial: an undeleted-but-busy retry after the bound just works, since the flag always clears.
