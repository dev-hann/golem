# golem

A lightweight, fast coding agent. Go server + TUI client.

[![CI](https://github.com/dev-hann/golem/actions/workflows/ci.yml/badge.svg)](https://github.com/dev-hann/golem/actions/workflows/ci.yml)
[![License: Apache-2.0](https://img.shields.io/badge/License-Apache--2.0-blue.svg)](LICENSE)

## Why

Existing coding agents force a tradeoff: heavyweight runtimes that eat hundreds of MB, or sluggish state management. golem takes a different shape:

- **~100–150 MB RSS** in normal use (single static binary, no runtime to load)
- **Server/TUI split** — the agent server owns state; close your TUI, sessions live on. Reconnect anytime.
- **Append-only JSONL sessions** — no database, instant replay, trivially inspectable
- **Multi-provider** — Anthropic today; OpenAI, Z.ai (GLM), Ollama behind the same interface (roadmap phase 5)
- **MCP plugins** — standard Model Context Protocol servers for extensibility (roadmap phase 6)
- **Fast startup** — no VM warmup, connect and go

## Status

Pre-release. Under active development following [docs/ROADMAP.md](docs/ROADMAP.md).

## Install

Requires Go 1.25+.

```bash
go install github.com/dev-hann/golem/cmd/golem@latest
```

Or grab a release tarball ([releases](https://github.com/dev-hann/golem/releases); `golem` and `golem-tui` ship together for linux/darwin on amd64/arm64):

```bash
curl -L https://github.com/dev-hann/golem/releases/latest/download/golem_$(VERSION)_$(OS)_$(ARCH).tar.gz | tar xz
```

Or from a clone:

```bash
git clone https://github.com/dev-hann/golem
cd golem
make build
```

`golem version` reports the build; on launch golem checks for a newer release once a day and prints an upgrade hint (`GOLEM_DISABLE_UPDATE_CHECK=1` silences it). `golem update` performs the upgrade: it downloads the release tarball for your platform, verifies the sha256 against the release checksums, and replaces the binaries in place.

## Usage

```bash
golem    # first run: pick a provider (zai, anthropic, openai, ollama), then the TUI
```

The first run walks you through provider setup — keys are validated live and stored only as `ref:ENV_VAR` references (see [docs/CONFIG.md](docs/CONFIG.md)). Later runs skip straight to the TUI. `golem setup` re-runs the wizard anytime.

The first run spawns a detached `golem serve` (unix socket) and waits for it; later runs reuse the running server. Sessions survive closing the TUI — reconnect with `golem` anytime; stop the server with `pkill -f 'golem serve'`.

Also available: `golem serve` runs just the agent server; `golem chat` is a line-based REPL against a running server (`/quit` exits); `golem ask "<prompt>"` runs a one-shot prompt without the server. Tools execute autonomously — every call is shown in the transcript via tool blocks (ADR-0016).

### TUI keys

The keymap mirrors [opencode's defaults](https://opencode.ai/docs/keybinds/) wherever golem has the feature.

| Key | Action |
|---|---|
| `ctrl+p` | command palette — type to filter (`ns` fuzzy-matches "new session"), `↑`/`↓` or `ctrl+p`/`ctrl+n` select, `enter` run, `esc` close |
| `ctrl+x` … `n` | new session (transcript resets, same connection) |
| `ctrl+x` … `l` | session picker — type to filter, `ctrl+r` rename, `↑`/`↓` select, `enter` open, `esc` clear filter/close |
| `ctrl+x` … `m` | model picker — switches the session's model from the next turn (`set_model`) |
| `ctrl+x` … `q` / `ctrl+d` | quit |
| `enter` | send message |
| `alt+enter` / `ctrl+j` | insert a newline — the input box grows with the message, wrapped lines included |
| `ctrl+c` | interrupt the active turn; then clear the composer; quit once it is empty |
| `esc` | interrupt the active turn |
| `pgup` / `pgdn` | scroll the transcript |
| `ctrl+g` / `ctrl+alt+g` | jump the transcript to the top / bottom |
| click a tool row | expand / collapse that tool call's result — `▸ ✓ read main.go` summaries are collapsed by default (✗ marks failures) |

All leader commands share the `ctrl+x` prefix: press it, then the key
within two seconds (the composer hint lists the options while the
sequence is pending). Direct chords like `ctrl+m` cannot be bound —
terminals send them identically to `enter` — which is why the command
namespace lives behind the leader. The `ctrl+p` palette doubles as the
key reference; tool-call results render inline under their summary line.

## Configuration

Config files ([docs/CONFIG.md](docs/CONFIG.md) is the spec) land in [roadmap phase 5](docs/ROADMAP.md). Today golem is configured by environment variables only:

| Variable | Purpose |
|---|---|
| `ANTHROPIC_API_KEY` | provider API key (required) |
| `GOLEM_MODEL` | model override (default `claude-opus-5`) |

Only the Anthropic provider is implemented so far; see [docs/ROADMAP.md](docs/ROADMAP.md) for the rest.

## Documentation

- [docs/ARCHITECTURE.md](docs/ARCHITECTURE.md) — system design, modules, data flow
- [docs/PROTOCOL.md](docs/PROTOCOL.md) — the NDJSON wire protocol
- [docs/DECISIONS.md](docs/DECISIONS.md) — architecture decision records
- [docs/ROADMAP.md](docs/ROADMAP.md) — development phases and acceptance criteria
- [docs/CONFIG.md](docs/CONFIG.md) — configuration and plugin lifecycle

## Contributing

Development is test-driven and doc-first. See [CONTRIBUTING.md](CONTRIBUTING.md) and [AGENTS.md](AGENTS.md).

## License

Apache-2.0. See [LICENSE](LICENSE).
