# golem

A lightweight, fast coding agent. Go server + TUI client.

[![CI](https://github.com/golem-agent/golem/actions/workflows/ci.yml/badge.svg)](https://github.com/golem-agent/golem/actions/workflows/ci.yml)
[![License: Apache-2.0](https://img.shields.io/badge/License-Apache--2.0-blue.svg)](LICENSE)

## Why

Existing coding agents force a tradeoff: heavyweight runtimes that eat hundreds of MB, or sluggish state management. golem takes a different shape:

- **~100–150 MB RSS** in normal use (single static binary, no runtime to load)
- **Server/TUI split** — the agent server owns state; close your TUI, sessions live on. Reconnect anytime.
- **Append-only JSONL sessions** — no database, instant replay, trivially inspectable
- **Multi-provider** — Anthropic, OpenAI, Z.ai (GLM), Ollama behind one interface
- **MCP plugins** — standard Model Context Protocol servers for extensibility
- **Fast startup** — no VM warmup, connect and go

## Status

Pre-release. Under active development following [docs/ROADMAP.md](docs/ROADMAP.md).

## Install

Requires Go 1.25+.

```bash
git clone https://github.com/golem-agent/golem
cd golem
make build
./bin/golem serve &   # agent server on a unix socket
./bin/golem-tui       # terminal client
```

## Configuration

See [docs/CONFIG.md](docs/CONFIG.md). Minimal setup:

```toml
# ~/.config/golem/config.toml
[provider]
default = "anthropic"

[provider.anthropic]
model = "claude-opus-5"
```

API keys come from environment variables (`ANTHROPIC_API_KEY`, `OPENAI_API_KEY`, `ZAI_API_KEY`); never store them in config files.

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
