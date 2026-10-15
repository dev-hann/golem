# Changelog

All notable changes to golem are documented here. The format follows
[Keep a Changelog](https://keepachangelog.com/en/1.1.0/); versions follow
[SemVer](https://semver.org/). Until the 1.0 release, the wire protocol and
config schema may change without notice.

## [Unreleased]

### Added — TUI session header (2026-10-15)

- The chat screen now shows a one-row session header above the
  transcript: the session title and the working directory's tail, with
  dim placeholders ("no session yet", "untitled session") until a title
  exists. The row is always present on terminals ≥ 16 rows and dropped
  on shorter ones, mirroring the status line's no-jump rule (ADR-0024).
- `session_meta` carries an optional `title` (the folded title at
  create/load), so a freshly loaded session is labeled immediately —
  no round-trip through `session_list`. The header also updates on
  `session_title` events for the current session and on optimistic
  picker renames; switching sessions (`ctrl+x n`, picker open, delete)
  resets it.

### Fixed — dead-looking turns: reasoning budget burn now fails loudly (2026-10-15)

- Sessions against GLM (zai/zai-coding) could go completely silent: the
  model, thinking enabled by default, spent its entire `max_tokens`
  budget on chain-of-thought streamed as the non-standard
  `delta.reasoning_content` — a field the OpenAI SDK drops — and the
  round ended with zero text, zero tool calls, and no error, rendering
  as a frozen session (ADR-0023).
  - The openai adapter now normalizes `reasoning_content` into
    `thinking_delta`, so reasoning time shows the "◐ reasoning" status
    instead of a dead screen.
  - A round truncated by `finish_reason=length` with no visible output
    errors (`llm.ErrEmptyReasoningRound`) instead of ending cleanly;
    any no-error round with neither text nor tool calls errors
    (`agent.ErrEmptyResponse`). The message names the cause and the
    remedies (raise `max_tokens`, disable thinking, compact the
    session — large accumulated contexts feed the burn).

### Fixed — focused picker rows rendered as color blocks (2026-10-15)

- The picker's focus highlight is reverse video, and any colored span
  nested inside it has its foreground turned into a background — the
  current-session `▎` bar became a bright cyan block (the glyph lost),
  the running-turn spinner and the dim age became colored rectangles,
  and the armed delete hint inverted into a red slab. Focused rows now
  render plain: the glyphs (`▎`, spinner, `ctrl+d again to confirm`)
  carry the state without color while highlighted, and the token colors
  return the moment the cursor moves off. Columns stay aligned either
  way — the plain glyphs occupy the same cells the styled ones do.

### Added — session picker: date groups and a scroll window (2026-10-15)

- The picker groups sessions by last-active date — `Today`,
  `Yesterday`, then `3d ago`…, then the calendar date past a week
  (opencode's shape, golem's relTime buckets). Headers ride the list
  flow: one per group boundary, never interleaved.
- Long lists no longer overflow the screen. The picker renders a
  15-line window (opencode's dialog maxHeight; clamped on short
  terminals) that follows the cursor, so the query line and key hints
  stay pinned no matter how many sessions exist. The mouse wheel
  scrolls the list freely while a modal is open (the transcript used
  to scroll behind it); any cursor move re-clamps the window, and a
  group entering view from above arrives with its header attached.

### Added — status line detail states: reasoning, synthesizing, retrying (2026-10-15)

- The agent status line now distinguishes five in-turn states instead of
  three. Two additive wire events drive the new ones; everything is
  forward-compatible (unknown types are skipped per PROTOCOL.md).
  - `thinking` — first token pending, as before (`turn_start`).
  - `◐ reasoning` — the model streams extended thinking
    (`thinking_delta`, new; anthropic adapter forwards its
    `thinking_delta` SSE kind as the event — content-free by design:
    reasoning text is never streamed, stored, or replayed).
  - `responding` — assistant text streaming (`text_delta`), as before.
    Reasoning followed by text shows responding — the answer began.
  - `◆ tool — arg` — tool running (`tool_call_start`), as before.
  - `◆ ✓/✗ tool · working` — synthesizing: the last tool finished
    (outcome mark) and the results are back at the model
    (`tool_call_end` until the next delta or start). Previously this
    window rendered a lying `responding`.
  - `↻ retrying n/N · wait` — the provider call failed transiently and
    the server announced an upcoming retry (`retry`, new:
    `attempt`/`retries`/`delay_ms`). The pre-content retry ladder can
    stall for ~61s; until now that whole window rendered a frozen
    `thinking`. A live retry outranks every other state while it runs;
    the next content event clears it.
- A busy session resumed mid-turn restores the detailed state from the
  replay: a trailing `tool_call_end` resumes as synthesizing, a trailing
  `thinking_delta` as reasoning (the fold skips both event types, so
  conversation state is unaffected).
- `internal/llm`: `Event` grows `Attempt`/`Retries`/`DelayMs` and a
  content-free `thinking_delta` type; `Retrying` emits `EventRetry`
  before each backoff sleep.

### Changed — status line drops the elapsed-seconds counter (2026-10-15)

- The active-turn status no longer shows a turn timer. The count reset to
  0s on every session switch: the start time lives only in the switching
  client (`time.Now()` at `turn_start`), while the wire protocol and the
  session store carry no timestamps — a resumed turn cannot know when it
  began, and the approximation (`time.Now()` on replay) rendered a fresh
  timer as if the turn had just started. Rather than carry a misleading
  number, the strip keeps what the protocol actually provides: state
  (thinking / responding / in-flight tool with its argument), the model
  at idle, and the last turn's usage. The per-second ticker
  (`statusTickMsg`, `armStatus`), the `turnStart`/`statusGen` fields, and
  their tests are removed with it. Restoring the timer would need a
  started-at timestamp on `turn_start`/`session_meta` — an additive
  protocol change, not taken here.
### Fixed — the session picker shows what you type (2026-10-15)

- The picker's search field vanished in the row redesign (aligned
  columns, age): typing filtered the list but nothing rendered the
  query, so a typo had no visible correction and "no matches" gave no
  clue why. The `› query_` prompt line is back (the command palette's
  shape), below the rows and above the key hints — it coexists with
  rename mode, where the selected row still swaps to the rename input.

### Added — session deletion (ctrl+d in the picker, 2026-10-15)

- `delete_session` removes a session from the server, opencode-style:
  a running turn is cancelled first and drained (bounded wait), then
  the JSONL file is deleted and confirmed with `session_deleted`.
  Deletion is permanent in the store: a racing or late writer (a turn
  goroutine, the title side call) gets `ErrDeleted` instead of
  re-creating the file — the append/check/open sequence and the flag
  sit under one lock, so no resurrection window exists (ADR-0019).
- The session picker binds `ctrl+d` to delete: the first press arms the
  row (its title swaps to `ctrl+d again to confirm` in the error
  color), the second removes it optimistically and sends the request;
  moving, typing, or `esc` disarms. Deleting the session you stand in
  resets to the lazy-start state — the next message creates a fresh
  session (ADR-0018). Confirmation from the server reconciles the list
  (another client's delete drops the row on arrival).

### Added — manual /compact command (2026-10-15)

- `/compact` in the TUI composer (and ctrl+p palette) and `golem chat` summarizes the session's history through a bounded provider side call (60s, 16 KiB summary) and appends a `session_compacted` barrier record (ADR-0022): storage stays append-only; the barrier drops pre-existing context on later turns, replaced by one `[Context summary]` message. Title and turn count survive; a failed summary emits `compact_failed` and leaves history untouched; refused while a turn runs or on an empty session. Automatic compaction stays deferred.

### Added — webfetch tool (2026-10-15)

- New read-only `webfetch` tool (ADR-0021): GET one absolute http(s) URL with a 30s timeout and 256 KiB body cap; HTML pages collapse to visible text (script/style stripped) via `golang.org/x/net/html`; redirect targets are re-checked per hop so a hop to `file:` or any non-http scheme is refused; non-2xx responses error. Registered for server turns and `golem ask`.

### Added — AGENTS.md instructions appended to the system prompt (2026-10-15)

- The agent now discovers `AGENTS.md` files and appends them to the system prompt of every turn (ADR-0020): `~/.config/golem/AGENTS.md` (global) first, `<session cwd>/AGENTS.md` (project) last — the nearer file reads as the more specific override. Each file is clipped at 32 KiB with a truncation marker; missing files are ignored, no configuration needed. Applies to server turns (discovered against the session's cwd) and `golem ask` (against the workspace root).

### Fixed — the status bar shows a running turn after switching back to its session (2026-10-14)

- Switching sessions while a turn was streaming and then returning to it
  rendered `● idle` even though the turn kept running. Cause: the turn's
  `turn_start` fires once, so nothing re-armed the active-turn state on
  return, and `load_session`'s replay folded away the lifecycle events
  the status line needed. `session_meta` now carries an additive `busy`
  flag (live state at load time), and the TUI's history replay folds
  turn lifecycle into status bookkeeping: the last `turn_start` restores
  the model name, the last `turn_end` the usage, and a busy session
  whose replay ends mid-turn resumes as an active turn (thinking /
  responding / tool). An idle load with a terminated replay
  stays idle.

### Changed — lazy session creation (2026-10-14)

- Clients no longer mint a session on connect. The TUI and `golem chat`
  create their session on the first submitted message (ADR-0018):
  launching and quitting without speaking leaves nothing on the server,
  and the session picker lists only sessions that hold a conversation.
  The first message of a session waits one round-trip
  (`create_session` → `session_meta`) before the turn starts, shown as
  `● creating session` in the status line; `ctrl+x n` before any session
  is a no-op, and a model picked before the session exists applies on
  creation.

### Fixed — the last transcript line stays visible under the sticky bar (2026-10-14)

- With the sticky header pinned, the viewport bottom-pinned against its
  full height while the render window ran one row shorter — the true
  last line sat below the fold, hidden behind the composer. The window
  math (`renderedWindow`) now pins the *rendered* window: at the tail it
  advances one row under the sticky bar; scrolled-back windows keep
  their top alignment (losing only the final row). Mouse mapping
  (`lineAt`/`lineClamped`) reads the same window, so clicks land on the
  row the user sees. Found in review: the earlier re-pin test's fixture
  ended with a trailing newline, letting an empty line take the hit —
  real streams end mid-line.

### Added — screen padding (2026-10-14)

- The chat screen now sits inside a padding margin instead of touching
  the terminal edges: two blank columns each side, one blank row top and
  bottom, and one blank row between the transcript and the composer. The
  viewport and composer shrink by exactly that chrome (plus the status
  row) so the screen still fills the terminal height (no alt-screen
  jitter); mouse selection and tool-row clicks track the shifted
  transcript origin.

### Removed — no 50-round turn cap (2026-10-14)

- Turns are no longer capped at 50 tool rounds (`max tool rounds (50)
  exceeded`). The loop runs until the model stops calling tools; the guard
  against a runaway loop is cancellation (interrupt the turn), not a cap
  (ADR-0017). Long tasks finish in one turn instead of dying mid-flight.

### Fixed — viewport re-pins the tail on height changes (2026-10-14)

- Shrinking the terminal (or the composer growing over its content)
  stranded the last transcript line below the fold: the viewport kept its
  old scroll offset over a shorter window. Both paths now re-pin the tail
  while the reader was at the bottom (`AtBottom` judged against the *old*
  height — after the shrink the stale offset is past the new fold). A
  scrolled-back reader keeps their offset: follow stays suspended until
  they page down again.

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
- Session picker rows are visual columns now — `▎ ⠋ title  2h ago` — no
  text status words: a user-colored left bar marks the session this client
  is in (cursor prepositions on it too, so opening the picker lands on
  "where you are"), an accent braille spinner animates while a turn is
  live in that session, and the dim relative age stays on the right.
  The title alone absorbs truncation so the columns stay aligned.
- Spinner animation runs only while the picker is open (100 ms ticks,
  generation-guarded so a closed picker's ticks drop); for the current
  session the client's own turn state wins over the (possibly stale) list
  snapshot, other sessions use the server's flag.
- The cursor arrow (`▶`) is gone from every list — session picker, model
  picker, command palette, kit.Picker, and the wizard's numbered
  fallback. The cursor is reverse video alone, and every row now renders
  in one shared gutter (`kit.Row`: one flanking cell, cursor row and
  plain rows alike), so moving the cursor highlights without shifting a
  single cell — the panel no longer re-widths and re-centers per move.

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
