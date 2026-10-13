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

- `golem serve` exits on SIGINT/SIGTERM. Previously the accept loop was
  never unblocked after shutdown, so the server ignored signals and left a
  stale socket file behind.
