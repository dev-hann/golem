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
- **Decision:** `delete_session` cancels a running turn first, waits for its goroutine to drain (`turnState.done`, bounded by 10s against a wedged provider), removes the JSONL file, and confirms with `session_deleted` to the requester only. Deletion is permanent in the store: `Store.Delete` flags the session id under the same lock as the file removal, and `Append` holds the store lock across its flag-check-open-write sequence — a racing or late writer (a turn goroutine that outlived the drain bound, the 90s title side call) gets `ErrDeleted` instead of re-creating the file, so there is no resurrection window at all. The TUI deletes optimistically (opencode's pattern): `ctrl+d` arms the row — the title swaps to `ctrl+d again to confirm` in the error token — and the second press removes the row immediately and sends the request; any navigation or typing disarms. Deleting the current session resets the client to the lazy-start state (ADR-0018).
- **Consequences:** ADR-0008's append-only discipline is preserved — deletion is a whole-file removal, not record mutation. The confirmation goes to the requesting connection only (no broadcast in v1); other clients reconcile on their next `list_sessions`, and the deleted session's in-flight tail events are documented as droppable. The cancel-drain wait makes `delete_session` synchronous for busy sessions (worst case the 10s bound). A session id, once deleted, is never writable again in this store instance — acceptable because ids are minted ULIDs never reused; a server restart (fresh store) naturally clears the flags, and the files are already gone.

## ADR-0020: AGENTS.md discovery — workspace instructions appended to the system prompt

- **Status:** Accepted (2026-10-15)
- **Context:** The agent's behavior rules lived only in the built-in system prompt. Users could not add project-scoped conventions ("this repo uses table-driven tests", "never touch generated/"), and machine-level preferences had nowhere to live. The AGENTS.md convention (adopted across Claude Code, opencode, Cursor) is the emerging standard: a plain markdown file whose contents are injected as instructions.
- **Decision:** Every turn discovers instruction files and appends them after the system prompt, in order: `~/.config/golem/AGENTS.md` (global, XDG_CONFIG_HOME honored) first, then `<session cwd>/AGENTS.md` (project) last — the nearer file comes later, so it reads as the more specific override. Each file is clipped at 32 KiB with a truncation marker; missing or blank files are ignored. `golem ask` discovers against its workspace root; the server discovers against the session's recorded cwd on every turn (so a file created mid-session applies immediately). `internal/agent.DiscoverInstructions(cwd)` owns discovery; `WithInstructions` threads it into the request's system prompt.
- **Consequences:** Users get per-project and per-machine agent rules with zero config surface — dropping a file is the whole interface. Every turn re-reads the files (two small reads per turn, negligible against provider latency) at the cost of consistent freshness. Prompt injection is a property of the format, not an accident: a workspace AGENTS.md is trusted content, same tier as the source files the agent reads. The global file's precedence-by-position (not by replacement) means both files always contribute.

## ADR-0021: webfetch tool and the golang.org/x/net dependency

- **Status:** Accepted (2026-10-15)
- **Context:** The agent could read the local workspace but not the web — no way to check a doc page, an error message, or a changelog without shelling out through bash curl. A read-only fetch needs three things stdlib alone makes painful: an HTML parser robust to real-world broken markup, strict scheme policing across redirects, and body caps. AGENTS.md requires an ADR for any new dependency; `docs/ARCHITECTURE.md` pins the allowed set.
- **Decision:** Add `internal/tools.WebFetchTool` — a read-only GET for one absolute http(s) URL: 30s timeout, 256 KiB body cap, redirect chain re-checked per hop (a redirect to `file:` or any non-http scheme is refused, as are non-http input URLs), HTML collapsed to visible text with script/style/noscript/template contents stripped. Non-2xx responses error. `golang.org/x/net/html` (html.Parse + a text-node walk, tag-strip fallback on unparseable input) enters the allowed dependency set for this purpose. Registered in the server and `golem ask` registries.
- **Alternatives considered:** stdlib-only tag stripping (fragile against malformed markup, no entity handling, no DOM awareness to skip script bodies); curl through the bash tool (adds a process spawn per fetch, no caps, leaks output to the transcript unbounded).
- **Consequences:** One maintained dependency, aligned with the Go project itself, no transitive baggage beyond what the ecosystem already carries. The tool is read-only: no headers, cookies, or credentials — SSRF surface equals what bash already allows locally, so no additional hardening beyond scheme policing and size/time caps is warranted for the local single-user threat model. HTML pages arrive as scannable text instead of markup soup; JSON/plain pass through untouched.

## ADR-0022: Manual context compaction via an append-only barrier event

- **Status:** Accepted (2026-10-15)
- **Context:** Long sessions grow until the model window fills (ADR-0017 made long turns routine; auto-compaction stays deferred — ROADMAP). Session storage is append-only (ADR-0008): rewriting files to drop old turns would break crash safety and the "storage lines are the wire events" invariant. The user, seeing context pressure, needs a manual escape hatch now; an automatic policy needs tuning data no one has yet.
- **Decision:** A `compact_session` client event triggers a bounded provider side call (60s timeout, 16 KiB summary cap, dedicated summary system prompt) over the session's folded history. On success the server appends a `session_compacted` record — a barrier: folding drops every conversation event before it and replaces the model's context with one synthesized user message (`[Context summary]\n…`); title, turn count, and cwd survive, `firstUserText` resets. On failure (or a refused request — turn running, empty session, unknown session) the client gets `compact_failed` / `error` and the history is untouched. Storage is never rewritten; the barrier replays inside `history` batches so reconnecting clients see the same fold. Clients: TUI `/compact` (composer + ctrl+p palette) and `golem chat` `/compact`.
- **Alternatives considered:** rewriting the JSONL on compaction (violates ADR-0008's append-only invariant and the crash-repair story); automatic compaction at a token threshold (no data to tune it, surprising mid-task context loss — kept deferred in ROADMAP).
- **Consequences:** Compaction is user-triggered and lossy by design: the dropped turns remain in the file (recovery/audit) but never re-enter context. The summary quality bounds how well the model continues; a bad summary is recoverable by starting a new session. One extra provider call per compaction. Fold logic gains one event type; replaying clients must apply the same barrier rule or they would render dropped turns the server no longer sends context for.

## ADR-0023: Reasoning streams must be visible; empty rounds must fail loudly

- **Status:** Accepted (2026-10-15)
- **Context:** GLM models (zai/zai-coding) ship with thinking enabled by default and stream chain-of-thought as `delta.reasoning_content` — a field the OpenAI Go SDK does not model, so it was silently dropped. Reasoning tokens consume the `max_tokens` budget; a hard-thinking round can spend all 16384 tokens invisibly, ending with `finish_reason=length`, zero text, zero tool calls. The agent loop treated that as a clean (if empty) round and emitted `turn_end`: the session appeared completely dead — no text, no error, no status change. A real session (markdown-styling plan) hit exactly this three turns in a row after its context grew past ~54k tokens of accumulated history.
- **Decision:** Two guards. (1) The openai adapter normalizes `reasoning_content` (read from the SDK's `JSON.ExtraFields` metadata) into `thinking_delta` events, so reasoning time renders as the existing "◐ reasoning" TUI state instead of a frozen screen. (2) Rounds that end truncated (`finish_reason=length`) with no text and no tool calls error with `llm.ErrEmptyReasoningRound`; independently, the agent loop turns any no-error round that produced neither text nor tool calls into `agent.ErrEmptyResponse` — a `provider_error`/`error` event reaches the client instead of a silent `turn_end`. The truncation guard stays out of the retry wrapper's transient set: retrying an identical request would burn the same budget again.
- **Alternatives considered:** raising `DefaultMaxTokens` (treats the symptom, costs every user tokens; the empty-round error still needs to exist for whatever cap is set); disabling thinking per-provider (GLM's coding endpoints default it on, and `thinking` is a non-standard body field the SDK does not model — needs the same ExtraFields workaround anyway); auto-retrying with a higher cap (duplicate transcript risk ADR-0017 already worried about, plus unbounded spend).
- **Consequences:** Dead-looking sessions now surface a diagnosable error naming the cause and remedies (raise `max_tokens`, disable thinking, or compact the session — huge input contexts feed the reasoning burn). `thinking_delta` events remain content-free on the wire (PROTOCOL.md unchanged in that respect). One behavioral fix in existing tests: a usage-only round (no text) now errors by design, so the usage-render fixture gained a text delta.
