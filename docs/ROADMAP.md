# golem Roadmap

Last updated: 2026-10-12. Each phase lists deliverables, **acceptance criteria**, and the **test list** (TDD: tests are the specification — write them first). A phase is done when all acceptance criteria pass and `make all` is green.

Convention used below: package paths are the unit of work; test names are indicative but the listed behaviors are the contract.

---

## Phase 1 — Core: providers, agent loop, built-in tools (CLI harness)

Status: implemented — all test-list items green, `make all` passes, coverage ≥ 80%. Remaining before done: live smoke test of `golem ask` against the real Anthropic API (manual, needs ANTHROPIC_API_KEY).

Goal: a working agent via `golem ask "..."` — no server, no TUI.

### Deliverables

- `internal/llm`: `Provider` interface, `Request`, `Event` types; `anthropic` adapter (reference implementation).
- `internal/tools`: read, write, edit, bash, grep, glob. Registry.
- `internal/agent`: turn loop with tool dispatch.
- `internal/approval`: policy engine (auto-mode for CLI harness with prefix whitelist; interactive gate lands with server/TUI). *(Removed 2026-10-14 — ADR-0016.)*
- `cmd/golem`: `ask` subcommand (one-shot).

### Acceptance criteria

1. `golem ask "read go.mod in <dir> and tell me the module name"` performs a real tool round-trip and prints the final answer.
2. Edit tool refuses (with clear error result to the model) on: missing old string, ambiguous multiple matches — model receives the error and can retry.
3. Bash tool with a non-whitelisted command in non-interactive mode is denied without execution. *(Superseded by ADR-0016: bash executes autonomously.)*
4. Grep/glob results are truncated to the byte budget; truncation is visible in the result.
5. Context cancellation (SIGINT) stops the loop within one await point.

### Test list

**internal/tools**
- `TestRead_ReturnsContentWithLineNumbers`
- `TestRead_ErrorsOnMissingFile`
- `TestWrite_CreatesAndOverwrites`
- `TestEdit_ReplacesUniqueString`
- `TestEdit_ErrorsWhenNotFound`
- `TestEdit_ErrorsWhenAmbiguous`
- `TestBash_CapturesStdoutAndStderr`
- `TestBash_RespectsTimeout`
- `TestBash_KillsProcessGroupOnCancel`
- `TestGrep_MatchesPattern_ReturnsFileLineResults`
- `TestGlob_MatchesRecursivePatterns`
- `TestTruncation_RespectsByteBudget_MarksTruncated`

**internal/approval** *(package removed 2026-10-14 — ADR-0016)*
- `TestApproval_AutoAllowsWhitelistedPrefixes`
- `TestApproval_DeniesNonWhitelistedInNonInteractive`
- `TestApproval_PrefixMatchRequiresWordBoundary`

**internal/agent**
- `TestLoop_TextOnlyTurn_EmitsDeltasThenEnds`
- `TestLoop_ToolRoundTrip_AppendsResultAndRepeats`
- `TestLoop_ToolsExhausted_ReturnsErrorToModel`
- `TestLoop_Cancel_StopsAtAwaitPoint`
- `TestLoop_TurnEndReportsUsage`

**internal/llm (anthropic adapter, against a stub HTTP server)**
- `TestAnthropicStream_NormalizesTextDeltas`
- `TestAnthropicStream_NormalizesToolUseBlocks`
- `TestAnthropicStream_SurfacesAPIError`
- `TestAnthropicStream_ContextCancelClosesChannel`

---

## Phase 2 — Sessions and server (unix socket, NDJSON)

Status: implemented — all test-list items green, `make all` passes, coverage ≥ 80%. `golem serve` + `golem chat` wired. The approval gate this phase shipped was later removed (ADR-0016); tools execute autonomously and report via tool_call events.

Goal: `golem serve` + protocol over the socket, testable with `nc`.

### Deliverables

- `internal/session`: JSONL append, replay, bounded cache, ULID ids.
- `internal/server`: unix socket listener, connection loop, event routing per PROTOCOL.md.
- `pkg/golemclient`: protocol types + blocking test client (used by tests and later by the TUI).
- `internal/cli` + `cmd/golem`: `serve`, `chat` (line-based REPL over the socket; subcommand logic lives in `internal/cli`, `cmd` stays wiring-only).

### Acceptance criteria

1. Two concurrent clients on separate sessions interleave without cross-talk (`session` scoping holds).
2. Kill -9 the server; restart; `load_session` replays the transcript exactly.
3. Every protocol event in PROTOCOL.md has a round-trip test (emit → parse → assert fields).
4. Unknown event type from a client is ignored (logged), connection survives.

### Test list

**internal/session**
- `TestAppend_WritesOneJSONPerLine`
- `TestReplay_FoldsEventsIntoConversation`
- `TestReplay_UnknownEventSkipped`
- `TestCache_EvictsLeastRecentlyActive`
- `TestLoad_StreamsFromDiskOnCacheMiss`

**internal/server**
- `TestServer_UserMessageProducesTurnLifecycle` (turn_start → deltas → turn_end)
- `TestServer_ApprovalRequestBlocksUntilResponse` *(removed — ADR-0016; see `TestServer_BashRunsDirectly_NoApprovalRoundTrip`)*
- `TestServer_ApprovalTimeoutDefaultsDeny` *(removed — ADR-0016)*
- `TestServer_InterruptEndsTurn_Gracefully`
- `TestServer_SessionListOrdersByLastActive`
- `TestServer_ConcurrentSessionsNoCrossTalk`
- `TestServer_IgnoresUnknownEventTypes`
- `TestServer_EventsPersistedBeforeEmission` (kill between write & emit → replay consistent)

**pkg/golemclient**
- `TestRoundTrip_AllClientEvents`
- `TestRoundTrip_AllServerEvents`
- `TestClient_ReconnectsAndLoadsHistory`

---

## Phase 3 — TUI

Status: implemented — all test-list items green, `make all` passes, coverage ≥ 80%. `golem-tui` binary wired (Alt-screen Bubble Tea app over the default socket). Flicker acceptance is inherently visual; verified via incremental-delta and 500-delta scrollback tests.

Goal: daily-driver terminal client.

### Deliverables

- `internal/tui` (behind a thin `cmd/golem-tui` binary): Bubble Tea app — chat view with streaming markdown-ish rendering *(made concrete as Phase 3.5, ADR-0026)*, collapsible tool-call blocks, input box, `/sessions` picker, interrupt key.
- `pkg/golemclient`: streaming (non-blocking) client API.

### Acceptance criteria

1. Streaming text renders incrementally without flicker at ≥ 200 deltas/turn.
2. Tool calls render collapsed with expand (one-line summary: tool + args + ok/err).
3. `ctrl+c` during a turn interrupts; prompt returns.
4. TUI restart reconnects to a live server and restores the open session via history replay.

### Test list

- `TestModel_AppliesTextDelta_UpdatesView`
- `TestModel_ApprovalPromptYesRunsCommand` *(removed — ADR-0016)*
- `TestModel_ApprovalPromptDenySendsDeny` *(removed — ADR-0016)*
- `TestModel_ToolCallCollapsedByDefault`
- `TestModel_InterruptSendsInterruptEvent`
- `TestModel_SessionPickerListsAndSwitches`
- `TestModel_HistoryReplayRendersTranscript`
- `TestViewport_HandlesLongOutputScrollback`

---

## Phase 3.5 — Assistant markdown rendering (ADR-0026)

Status: implemented — all test-list items green, `make all` passes, package coverage 93.9%. Tables added by ADR-0027 (see the table test list below).

Goal: assistant messages render markdown in the TUI — streaming-safe, cache-bounded, kit-token-only. Protocol and storage stay raw text.

### Deliverables

- `internal/tui/markdown`: block parser (paragraph, heading, fenced code, list, quote, hr; live-tail split), inline parser (bold/italic/code/link), kit-driven renderer with content+width caching.

### Acceptance criteria

1. Assistant blocks render headings/bold/code/lists from the same wire events; user/tool/error blocks unchanged.
2. Per-delta work is bounded to the trailing partial block (completed blocks cached by content+width).
3. Output uses kit tokens only — no raw ANSI escapes inserted by the markdown package; degrades cleanly under NO_COLOR (Ascii profile).
4. Protocol/JSONL unchanged; history replay renders markdown identically.
5. `make all` green, coverage ≥ 80%.

### Test list

**internal/tui/markdown**
- `TestParse_BlockKinds_Table`
- `TestParse_OpenFence_IsCodeBlock`
- `TestParse_InlineSpans_Table`
- `TestRender_UsesKitTokens_NoRawEscape`
- `TestRender_WrapsToWidth_ListHangingIndent`
- `TestRender_CodeBlockFrameAndLanguageLabel`
- `TestStream_CompletedBlocksCached_TailOnlyReparsed`
- `TestStream_UnclosedEmphasis_LiteralOutput`
- `TestRender_NoColorProfile_Degrades`

**internal/tui (integration)**
- `TestRenderBlocks_SpansRecomputedWithMarkdownHeight`
- `TestHistoryReplay_RendersMarkdown`

### Extension: markdown tables (ADR-0027)

Status: implemented — parser, renderer, and streaming rules extended; `make all` green.

**internal/tui/markdown**
- `TestParse_Table`
- `TestParse_TrailingTableLive`
- `TestRender_BlockShapes` (table grid + inline-marker cells)
- `TestStream_TableStreaming`
- `TestStream_PipesWithoutDelimiterStayLiteral`
- `TestParse_LineAfterBlock_NotDropped` (lossless-parse bug found on the way)

**internal/tui (integration)**
- `TestRenderBlocks_TableMarkdown`

---

## Phase 4 — Session management polish

### Deliverable

- `/new`, `/resume` commands. *(The approval UX originally planned here was cancelled — the approval gate was removed entirely; see ADR-0016.)*

### Acceptance criteria

1. `/new` starts a fresh session from the TUI; `/resume` restores a prior one.

---

## Phase 5 — Remaining providers + config system (in progress: config bootstrap landed 2026-10-13)

### Deliverables

- `internal/llm`: `openai`, `zai`, `ollama` adapters.
- `internal/config`: layered TOML merge, `ref:` secrets, env overrides.
- `golem chat` picks provider per config; `/model` switch at runtime.

### Acceptance criteria

1. Same integration script (ask/read/edit flow) passes on all four providers using their stub servers; one live smoke test per provider (manual, documented).
2. Config precedence test: defaults < global < project < env < flag, deep-merged maps, replaced slices.
3. `ref:SOME_KEY` resolves from env; missing ref is a startup error naming the key.

### Test list

- `TestConfig_PrecendenceFullChain`
- `TestConfig_DeepMergeMaps_ReplaceSlices`
- `TestConfig_RefSecretResolution_MissingKeyFails`
- `TestOpenAIAdapter_NormalizesToolCalls` (stub)
- `TestZaiAdapter_UsesBaseURLOverride` (stub)
- `TestOllamaAdapter_NormalizesStream` (stub)
- `TestProviderSwitch_MidSessionKeepsHistory`

---

## Phase 6 — MCP plugins

### Deliverables

- `internal/mcp`: client (stdio JSON-RPC), lifecycle state machine, tool discovery & registration as `mcp_<server>_<tool>`.
- Config: `[plugins.mcp.<name>]` blocks per CONFIG.md.

### Acceptance criteria

1. Declared MCP server starts lazily, tools appear in the registry, agent uses them in a turn.
2. Plugin crash → exponential backoff restart (max 3) → marked failed; its tools return a clear error result to the model.
3. Server shutdown stops plugins gracefully (5 s timeout → SIGKILL).
4. MCP tools execute autonomously like built-in tools; invocations are observable via tool_call events (ADR-0016).

### Test list

- `TestMCP_HandshakeListsTools`
- `TestMCP_ToolCallRoundTrip` (against a scripted fake MCP server binary in testdata)
- `TestMCP_LazyStartOnFirstSessionUse`
- `TestMCP_CrashRestartBackoff_ThenFailed`
- `TestMCP_GracefulShutdownTimeoutKills`
- `TestMCP_Register_NamesMangledCorrectly`

---

## Phase 7 — Hardening & release prep (partial: release pipeline + version/update-check landed 2026-10-13; replay benchmark landed 2026-10-16)

### Deliverables

- `GOMEMLIMIT` enforcement from config; session cache bound; tool-output budget.
- Packaging: release binaries (goreleaser or plain `make dist`), Homebrew formula (optional).
- README quickstart verified end-to-end.

### Acceptance criteria

1. Idle server RSS < 50 MB; 10-session soak < 150 MB.
2. Benchmark: session replay of 500-turn JSONL < 100 ms.
3. `make all` green from clean clone on Go 1.25.

### Test list

- `TestMemLimit_SetsGOMEMLIMIT`
- `TestSessionCache_BoundRespected`
- `TestReplayBenchmark_500Turns` (Go benchmark with budget assertion) — done, 2026-10-16 (ADR-0029; ~3 ms)

---

## Landed outside the phase plan

- 2026-10-12 — `golem serve` shutdown fix: SIGINT/SIGTERM now stop the server (the listener closes and the accept loop unblocks; regression-tested via the injected signal hook).
- 2026-10-13 — Bare `golem` launcher: probe the default socket, spawn a detached `golem serve` when it is down (removing stale socket files), then run the TUI. Ships ahead of phases 4/5; `golem stop` and config-driven socket paths land with phase 5.
- 2026-10-14 — TUI agent status line: a one-row strip between the transcript and the composer, always rendered (idle shows the model and last-turn token usage; an active turn shows thinking → responding → the in-flight tool with its identifying argument). Agent state only — no key hints. Fully derived from existing protocol events (turn_start/text_delta/tool_call_start/tool_call_end/turn_end) — no wire changes. Ticker follows the leader-timeout gen pattern for stale-tick safety. (The elapsed-seconds counter was dropped on 2026-10-15 — it reset on every session switch because the true start time never survives the replay.)
- 2026-10-15 — Status strip animation: the leading glyph of every in-progress state is now the animated braille spinner (100 ms frames) — idle keeps the static `●`. The picker's ticker was generalized into one shared animation ticker (frame counter + generation guard) driving both surfaces; `turn_start`, a busy-session resume, and opening a picker with running sessions arm it, and turn end orphans the generation.
- 2026-10-15 — TUI session header: a one-row title strip above the transcript (title + cwd tail; dim placeholders for the no-session and untitled states), always rendered on terminals ≥ 16 rows and dropped below that floor (ADR-0024). `session_meta` gains an optional `title` field so a loading client labels the session at once; the header also updates from `session_title` events and optimistic picker renames.
- 2026-10-15 — TUI sticky question line adopts push-out semantics (ADR-0028): the pin holds the latest user bar pushed above the *rendered window's top* — submitting the next question no longer clears it (the old rule cleared at the turn-start follow snap with a one-row jump); the pin swaps in place when streamed content pushes the new bar past the top.
- 2026-10-16 — Session cache folds incrementally (ADR-0029): a cached session keeps its live fold and a byte frontier, so `Get` after appends folds only the new lines instead of reloading the whole file; `List` summaries cache per file by (size, mtime). The server's post-turn/rename/compaction `Invalidate` calls are gone — `Get` is always fresh. A 500-turn cold replay folds in ~3 ms (Phase 7 budget 100 ms, `TestReplayBenchmark_500Turns`).

## Explicitly deferred (needs new ADR before starting)

Sub-agents, LSP integration, remote/web auth, Windows named pipes.
