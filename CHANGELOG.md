# Changelog

All notable changes to golem are documented here. The format follows
[Keep a Changelog](https://keepachangelog.com/en/1.1.0/); versions follow
[SemVer](https://semver.org/). Until the 1.0 release, the wire protocol and
config schema may change without notice.

## [Unreleased]

### Removed — no 50-round turn cap (2026-10-14)

- Turns are no longer capped at 50 tool rounds (`max tool rounds (50)
  exceeded`). The loop runs until the model stops calling tools; the guard
  against a runaway loop is cancellation (interrupt the turn), not a cap
  (ADR-0017). Long tasks finish in one turn instead of dying mid-flight.

### Fixed — history replay opens at the bottom (2026-10-14)

- Loading a session (session picker → enter, or a TUI restart reconnecting
  to a live server) rebuilt the transcript but left the viewport at its
  previous offset — usually the top. Replay now restores auto-follow and
  pins the viewport to the tail, matching where a live stream would have
  left the view: the conversation continues from the end.

### Added — session picker current/status markers (2026-10-14)

- `session_list` rows carry a `busy` flag — live server state: true while a
  turn runs in that session, false on idle. A list-time snapshot, not a
  subscription.
- The session picker marks the session this client is in with `· current`
  and prepositions the cursor on it, so opening the picker lands on "where
  you are" instead of row zero.
- Sessions with a live turn show `· running` in the picker. For the current
  session the client's own turn state wins over the (possibly stale) list
  snapshot; other sessions use the server's flag.

### Added

- Agent status line in the TUI: a one-row strip between transcript and
  composer. Idle shows the model and the last turn's token usage
  (↑in ↓out); while a turn runs it shows `thinking` → `responding` → the
  in-flight tool name and its identifying argument with elapsed seconds.
  The strip carries agent state only — no key hints. Layout height stays
  constant (the row is borrowed from the viewport, like the sticky
  header).

### Fixed

- Status-line state leaked across session switches: picking a session
  from the picker cleared the transcript but kept the previous
  session's model name and token usage in the idle status. Both switch
  paths (new session, picker load) now go through `clearTranscript`,
  which resets every session-scoped field.

- The composer now grows with **soft-wrapped** lines too, not just explicit
  newlines: a long single line that the textarea wraps over several screen
  rows expands the input box (same min-height, 40% terminal cap, and
  viewport give-back as before). The row count comes from the textarea's own
  wrap — bubbles breaks lines that fill the width one row earlier than plain
  width math (trailing-space anchors, inclusive boundaries, nine Hangul
  syllables per 20-column row), so the box always matches the rendered
  content and never hides its top rows.
- `SocketOwnerPID` now tolerates lsof builds that append ` type=STREAM` to
  the `-F n` name field — foreign-server detection works outside CI's lsof.

### Removed — approval gate (ADR-0016)

- The interactive approval gate is gone: tools execute autonomously and are
  observable via `tool_call_start`/`tool_call_end` transcript blocks.
- Removed the `internal/approval` package, the agent-loop bash gate, the
  `approval_request`/`approval_response` protocol events and
  `approval_timeout` error code, the `[approval]` config section, and the
  TUI approval modal. Old sessions replay cleanly (unknown events skipped).

### Added — phase 1 (agent core)

- `Provider` interface with the Anthropic adapter as reference implementation.
- Agent turn loop with tool dispatch and cancellation.
- Built-in tools: read, write, edit, bash, grep, glob — with output budgets.

### Added — phase 2 (sessions & server)

- Append-only JSONL session store with ULID ids and bounded in-memory cache.
- Unix-socket NDJSON server per `docs/PROTOCOL.md`.
- `pkg/golemclient`: public protocol types and typed client library.
- `golem serve` and `golem chat` subcommands.

### Added — phase 3 (TUI)

- `golem-tui` (Bubble Tea): streaming transcript, collapsible tool blocks,
  approval prompts (y/n), session picker, interrupt.
- `golem ask` one-shot prompt mode.

### Added — launcher (2026-10-13)

- Bare `golem` (and `golem tui`): probes the default socket, spawns a
  detached `golem serve` when no server is running (stale socket files are
  removed so the new listener can bind), then runs the TUI. Subsequent
  invocations reuse the running server.

### Fixed — tool-row click targeting off by one per tool block (2026-10-14)

- renderBlocks counted the tool attach separator ('\n' — a mere line
  break, not a blank row) as a line, shifting every span below a tool
  block by one row per tool call above it. Clicks landed on the row
  above the chevron actually hit. A new parity test clicks every
  rendered tool row across scroll offsets and asserts the toggled block
  matches the row's visible text; it also caught the same skew in the
  sticky-header's user spans.

### Changed — tool results collapse behind clicks (2026-10-14)

- Tool calls render one summary row — `▸ ✓ read main.go` — with the
  argument parsed per tool (path, command, pattern) instead of raw JSON.
  Results (success and failure alike) stay collapsed; clicking the row
  toggles the `▾` expansion with the bounded preview. Hit-testing reuses
  the sticky-header span tracking (toolSpans) and accounts for the
  pinned question bar.

### Added — provider retry with exponential backoff (2026-10-14)

- Rate limits, overload, 5xx, and network drops no longer kill a turn on
  first contact: `llm.Retrying` wraps every constructed provider and
  retries transient failures before any content has streamed — 1s, 2s,
  4s, 8s, 16s, 30s (six retries, ~61s worst case), then surfaces the
  last error. Failures after content began are not retried: the
  transcript already holds the partial output and a retry would
  duplicate it. Auth failures and request errors (4xx) fail immediately.
- Adapters classify SDK errors onto `llm.ErrTransient`/`llm.ErrAuth`
  markers (status-based; status-less errors count as network drops).
  Each retry leaves a line in serve.log. Applies to every consumer —
  turns, the session-title side call, chat, ask.
- The title side call's deadline now covers the retry ladder (90s vs
  the ladder's ~61s worst case): a rate-limited provider that would
  recover no longer loses the race against its own timeout.

### Added — session picker search/rename, server version handshake (2026-10-14)

- Session picker rows are minimal: `title · 2h ago` (directory name
  until the LLM title lands); turn counts and paths are gone from the
  row. Typing fuzzy-filters the open list; `esc` clears the filter
  first, then closes.
- `ctrl+r` in the picker renames the highlighted session inline
  (opencode's binding): enter commits via the new `rename_session`
  event — the server sanitizes, persists the newest `session_title`
  record (last one wins), and confirms with `session_title`.
- A stale detached server no longer serves a newer client silently:
  `version_query`/`server_hello` handshake plus a pid file next to the
  socket. The launcher restarts a mismatched server automatically — via
  the pid file, or by resolving the socket's owner with `lsof` when the
  pid file is absent (pre-pidfile servers). When neither yields a safe
  kill target the launch fails fast with the restart instruction instead
  of continuing degraded. Title-generation failures now leave a line in
  serve.log instead of vanishing.

### Added — LLM-generated session titles (2026-10-14)

- After a session's first turn the server makes a title side call
  (opencode-style prompt: one line, ≤50 chars, the user's language) and
  persists the result as a `session_title` record (folded back on load,
  listed in `session_list` via the new optional `title` field). Failures
  leave the session untitled within a turn — no fallback — but every
  later turn of an untitled session retries generation with the
  session's first user message. The title is never streamed mid-turn;
  the picker sees it on its next list.
- The session picker row is now `title · dir · N turns · 2h ago` — the
  raw ULID no longer renders. `sessionRow`/`relTime` in the TUI derive
  the row from `SessionSummary` (`last_active` was already on the wire,
  now finally shown).

### Removed — status footer (2026-10-14)

- The one-line status bar above the composer is gone: no more
  provider · model · thinking…/done (in=… out=…) line, and the
  `statusParts` state with it (it accumulated across turns anyway).
  The composer now meets the transcript directly and the viewport
  reclaims the row. `provider`/`model` stay as state — the model
  picker still shows them (`models · zai-coding`). `Kit.StatusBar`
  lost its only caller and is removed. Status surface may return
  later as a deliberate design, not as leftover chrome.

### Changed — keymap follows opencode, command palette replaces help (2026-10-14)

- `ctrl+p` opens a command palette (opencode `command_list`): type to
  filter with fuzzy subsequence matching (`ns` hits "new session"),
  `↑`/`↓` or `ctrl+p`/`ctrl+n` select, `enter` runs, `esc` closes. It
  lists new session, session list, model list, and quit with their
  shortcuts, doubling as the key reference.
- The `ctrl+x ?` help modal is gone — the palette carries the
  non-action hints (send, newline, jump keys) in its footer.
- `ctrl+x l` now opens the session picker (opencode `session_list`;
  was `ctrl+x p`). The clear-transcript-view command is removed —
  opencode has no counterpart and the session list covers the need.
- `ctrl+c` follows opencode's exit ladder: interrupt the active turn,
  then clear the composer, quit only once it is empty. `ctrl+d` and
  `ctrl+x q` also quit.
- `ctrl+j` inserts a newline alongside `alt+enter` (opencode
  `input_newline`).
- `ctrl+g` / `ctrl+alt+g` jump the transcript to the top / bottom
  (opencode `messages_first`/`messages_last`); plain home/end stay with
  the composer as in opencode. Dialogs (sessions, models, palette)
  navigate with `ctrl+p`/`ctrl+n` too.
- Tool-call blocks always show their result preview inline; the
  `ctrl+x x` expand/collapse toggle and the `expanded` block state are
  removed.

### Changed — leader mode marks the composer, not the status bar (2026-10-14)

- Arming a `ctrl+x` sequence no longer prints the key cheat sheet into the
  status bar above the composer. The mode now shows on the composer
  itself: its left rail turns accent-colored, and the empty-input hint
  swaps to "shortcut mode — press ? for the key list · esc cancels"
  (idle hint stays the enter/alt+enter/ctrl+x reference). The rail is the
  persistent indicator — the hint text only prints while the input is
  empty.

### Changed — leader-key command namespace (2026-10-13)

- Every command now rides a `ctrl+x` leader sequence: `ctrl+x n` new
  session, `p` session picker, `m` model picker, `x` expand/collapse the
  latest tool block, `l` clear the transcript view, `?` this reference.
  Press `ctrl+x`, then the key within two seconds; the status bar lists
  the options while the sequence is pending, `esc` cancels, any other
  key disarms and types normally. Direct chords (`ctrl+n/p/l`, `alt+m`,
  `tab`, `ctrl+/`) are gone — `ctrl+m` specifically is unbindable
  because terminals send it identically to `enter` (both CR, byte
  0x0D), which is what pushed every mnemonic behind the leader.
- Timeout ticks are generation-guarded: a stale timer cannot disarm a
  freshly armed sequence.

### Changed — transcript readability (2026-10-13, v0.1.9)

- Chat turns are visually separated: user messages carry a colored left
  bar (`kit.MessageBar` — wrapping keeps the bar on every line), assistant
  output stays plain, and blocks are split by blank lines with tool lines
  attached. The `you>`/`golem>` labels are gone.
- The most recent user message pins as a one-line sticky reminder under
  the header once it scrolls fully above the viewport (the viewport yields
  one row to keep the layout constant); scrolling back unpins it.

### Fixed — transcript scrolling (2026-10-13, v0.1.9)

- PgUp/PgDn no longer snap back to the tail while a turn streams: manual
  scrollback suspends the auto-follow, which resumes on reaching the
  bottom again or when a new message/turn starts.
- The mouse wheel scrolls the transcript (three lines per notch through
  the same path). Mouse cell motion is now enabled, so terminal-native
  text selection requires shift while the TUI runs.

### Added — centered modal system, model picker, help (2026-10-13, v0.1.8)

- Every overlay shares one shape: a background-filled `kit.Modal` panel
  floating over a faded backdrop of the full layout (ANSI-aware merge via
  `x/ansi`; the backdrop renders escape-free in the theme's dim gray —
  terminals cannot blur, and this stays terminal-independent). `ctrl+/`
  opens the binding reference; `alt+m` opens the model picker fed by the
  launcher (which fetches the configured provider's models, degrading
  silently), applying the choice via the new `set_model` event from the
  next turn. Note: `ctrl+m` is indistinguishable from `enter` in terminal
  protocols (both CR), so the m mnemonic rides the alt grammar with
  alt+enter.

### Added — keyboard-driven TUI controls (2026-10-13, v0.1.8)

- `ctrl+n` new session (transcript resets on the same connection; streaming
  left over from the previous session is dropped via a session guard),
  `ctrl+p` session picker, `ctrl+l` clear the transcript view, `esc`
  interrupts the active turn. The slash commands (`/quit`, `/exit`,
  `/sessions`) are gone — quitting is double `ctrl+c`, navigation is keys,
  and everything typed is message content. The composer placeholder
  documents the bindings.

### Changed — composer and platform (2026-10-13, v0.1.8)

- The composer is now a single-line rounded frame (`kit.Box`, Tokyo Night
  border) with no prompt prefix or line numbers; it grows only when the
  message has newlines.
- The test suite runs on macOS: unix-socket test paths stay under the
  104-byte `sun_path` limit via short temp dirs (`t.TempDir()` embeds
  test names and made every listen fail with EINVAL on a Mac).

### Added — multiline input (2026-10-13, v0.1.7)

- `alt+enter` (option+enter) inserts a newline; the composer grows with
  its content (floor of two rows, capped at 40% of the terminal) and
  returns the rows to the transcript on send. Plain `enter` still
  submits; multiline paste was already preserved and now has a visible
  home. Shift+enter is indistinguishable from enter in bubbletea v1's
  terminal protocol — hence the alt mapping.

### Changed — Tokyo Night default theme, full kit migration (2026-10-13, v0.1.6)

- The default palette is now Tokyo Night (Night for dark terminals, Day
  for light ones) — a designed scheme replacing the incidental ANSI
  colors the transcript happened to use.
- Every remaining surface renders through the kit: the TUI footer
  (approval prompt, session picker with a themed cursor, and a real
  status bar showing provider · model · state and turn usage), the
  message input (prompt/placeholder themed), and the wizard's prompts,
  errors, plan hints, and numbered fallback. Raw lipgloss usage outside
  `internal/tui/kit` is now zero.

### Added — themeable TUI design kit (2026-10-13, v0.1.5)

- `internal/tui/kit`: the design system every TUI surface renders through.
  Semantic color tokens, pure-data themes (a future `[theme]` config
  section is `ThemeFromMap` + `kit.New`; runtime switching constructs a new
  kit — no globals), derived styles, and shared components including the
  arrow-key picker with a themed selection cursor. The chat transcript,
  tool blocks, and the setup wizard are themed now; `NO_COLOR` and
  non-terminal output degrade to plain automatically.

### Added — Z.ai GLM Coding Plan preset (2026-10-13, v0.1.4)

- New `zai-coding` provider preset: the GLM Coding Plan endpoint
  (`https://api.z.ai/api/coding/paas/v4/`, default model glm-5.3) —
  coding-plan keys only work there, pay-per-use keys only on `zai`. Both
  presets share ZAI_API_KEY, so env detection is ambiguous by design and
  the setup wizard runs to pick a plan once; a rejected zai key now hints
  at the sibling preset. Note the plan's terms limit use to officially
  supported tools.

### Added — resilient serve, arrow-key picker, self-update (2026-10-13, v0.1.3)

- `golem update`: verified self-update from GitHub releases (platform
  tarball, sha256 against checksums.txt, atomic replace). Release builds
  no-op when current; snapshots always update.
- The wizard's model selector is a real arrow-key picker on terminals
  (bubbletea, no new dependency), falling back to numbered input without
  a TTY.
- Provider health: one background probe per registered provider at boot
  (ready / no key / key rejected / unreachable), held in a status
  registry; blocked providers fail turns with an actionable error.
  Credential problems never stop the server from booting.
- The spawned daemon logs to `~/.local/share/golem/serve.log`; every
  not-ready failure names it. The wizard warns when a running server
  predates the config it just wrote.

### Changed — API keys stored in config by default (2026-10-13)

- The setup wizard now writes the validated key as plaintext into the
  global config (mode 0600) instead of a `ref:ENV_VAR` indirection that
  required shell-profile exports and made daemon startup fail invisibly
  (ADR-0015; same shape as `gh` and `aws` CLI local credentials).
  `ref:` and env-only remain supported options; the plaintext warning is
  gone.

### Added — setup wizard model selector (2026-10-13)

- The wizard fetches the provider's live model list (`/v1/models`) and
  offers a numbered picker: filter (non-chat ids), sort, 30-entry cap,
  preset-default highlight, custom id entry. The fetch doubles as key
  validation — a 401/403 retries the key, a missing endpoint accepts it
  and falls back to manual model entry with the preset suggestion.
  Ollama lists local models when the endpoint is up.

### Added — release pipeline (2026-10-13)

- Tag pushes (`v*`) run goreleaser in CI: `golem` and `golem-tui` for
  linux/darwin on amd64/arm64, version/commit stamped via ldflags,
  checksums attached to the GitHub release. First release: v0.1.0.

### Added — version reporting + update check (2026-10-13)

- `golem version`: build version, commit, platform. Version resolves from
  ldflags (goreleaser, `make build` stamps git describe), then the module
  version embedded by `go install pkg@vX.Y.Z`, then `dev`.
- Launch and `golem version` check the latest GitHub release at most once
  a day (3s timeout, failures silent, `GOLEM_DISABLE_UPDATE_CHECK=1`
  disables) and print a one-line upgrade hint when a newer semver exists.
  Dev builds are never nagged.

### Added — config-driven provider wiring (2026-10-13)

- `golem serve` and `golem ask` resolve their provider through
  `cli.ResolveProvider`: GOLEM_PROVIDER > config `provider.default` >
  single-detected-env-key inference > the built-in default. Models follow
  GOLEM_MODEL > config > preset default; keys follow config `ref:` > the
  provider's env var. `turn_start` events now carry the real provider id
  (test-injected providers render as `custom`).

### Added — first-run provider onboarding (2026-10-13)

- Bare `golem` (and `golem setup`) runs a setup wizard when no config file
  exists and no provider key is in the environment: provider picker with
  key detection, model prompt with preset defaults, live key validation
  (3 attempts), config written with `ref:ENV_VAR` secrets only — the
  plaintext key never lands in the file, the wizard prints the `export`
  line for the shell profile instead. Ollama registers keyless.

### Added — provider presets + openai-compat adapter (2026-10-13)

- `internal/llm` presets: built-in catalog for `anthropic`, `openai`, `zai`
  (base URL + `ZAI_API_KEY` + default model), and `ollama` (keyless, local
  endpoint), with `DetectFromEnv` listing providers whose key is set.
- `internal/llm/openai`: one adapter for every OpenAI-compatible endpoint
  (ADR-0014) — streaming text/tool-call normalization, usage accounting,
  per-tool-result messages, context cancellation.

### Added — config bootstrap (2026-10-13)

- `internal/config`: built-in defaults, global `~/.config/golem/config.toml`
  parsing (`[provider]` + `[provider.<id>]` tables), `GOLEM_PROVIDER`
  override, and `ref:ENV_VAR` secret resolution (missing env var is a
  startup error naming the variable; plaintext keys warn).

### Fixed

- First-run wizard no longer hangs on a real terminal: prompts were
  written through a buffered writer and only flushed after the wizard
  returned, so the provider menu never appeared and golem sat blocked on
  stdin with a blank screen. Prompts now write unbuffered (regression
  test drives the wizard with no input available and requires the
  provider menu on stdout first).
- `golem version` exits on SIGINT/SIGTERM. Previously the accept loop was
  never unblocked after shutdown, so the server ignored signals and left a
  stale socket file behind.
