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
git clone https://github.com/dev-hann/golem
cd golem
make build
./bin/golem serve &   # agent server on a unix socket
./bin/golem-tui       # terminal client
```

## Usage

```bash
./bin/golem serve &   # agent server on a unix socket
./bin/golem-tui       # terminal client
```

`golem ask "<prompt>"` runs a one-shot prompt without the server; `golem chat` is a line-based REPL against a running server (`/quit` exits; command approvals are auto-denied until the phase 4 UX lands).

### TUI keys

| Key | Action |
|---|---|
| `enter` | send message |
| `ctrl+c` | interrupt the active turn; press again to quit |
| `pgup` / `pgdn` | scroll the transcript |
| `tab` | expand/collapse the most recent tool-call block |
| `y` / `n` | allow / deny a pending command approval (any other key denies) |
| `/sessions` | session picker — `↑`/`↓` select, `enter` open, `esc` cancel |
| `/quit`, `/exit` | quit |

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
