# Changelog

All notable changes to golem are documented here. The format follows
[Keep a Changelog](https://keepachangelog.com/en/1.1.0/); versions follow
[SemVer](https://semver.org/). Until the 1.0 release, the wire protocol and
config schema may change without notice.

## [Unreleased]

### Added — phase 1 (agent core)

- `Provider` interface with the Anthropic adapter as reference implementation.
- Agent turn loop with tool dispatch and cancellation.
- Built-in tools: read, write, edit, bash, grep, glob — with output budgets.
- Approval gate: read-only prefix whitelist, word-boundary matching, compound
  command segmentation, substitution/ redirection forced deny.

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

### Added — keyboard-driven TUI controls (unreleased)

- `ctrl+n` new session (transcript resets on the same connection; streaming
  left over from the previous session is dropped via a session guard),
  `ctrl+p` session picker, `ctrl+l` clear the transcript view, `esc`
  interrupts the active turn. The slash commands (`/quit`, `/exit`,
  `/sessions`) are gone — quitting is double `ctrl+c`, navigation is keys,
  and everything typed is message content. The composer placeholder
  documents the bindings.

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
