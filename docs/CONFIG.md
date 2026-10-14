# golem Configuration & Plugin Lifecycle

Last updated: 2026-10-12. Schema changes require updating this file in the same commit.

> **Status: partially implemented.** The config parser (`internal/config`) is bootstrapped: built-in defaults, the global file, `GOLEM_PROVIDER`/`GOLEM_MODEL` overrides, `[provider]` sections, and `ref:` secret resolution. Project files, CLI flags, and the remaining sections (`[server]`, `[tools]`, `[plugins]`) land with roadmap phases 4–6. Providers beyond Anthropic arrive in phase 5; MCP support in phase 6. This document is the contract those phases must implement against; schema changes still require updating this file in the same commit. Parsed by `internal/config` (`BurntSushi/toml`, ADR-0012).

## First-run onboarding

Bare `golem` (and `golem setup` at any time) runs a short wizard when no provider is configured — no config file and no provider key in the environment:

1. Pick a provider from the built-in catalog (detected keys are marked).
2. For keyed providers, paste an API key — golem proves it by fetching the provider's model list (`GET /v1/models`). A rejected key retries (3 attempts); if the endpoint offers no listing, the key is accepted and the model is typed manually with the preset default suggested.
3. Pick a model from the live list: a numbered selector (non-chat ids like embeddings filtered, sorted, capped at 30) — pick by number, type a custom id, or press enter for the highlighted preset default.
4. The config file is written with the validated key stored directly (`api_key = "..."`, file mode 0600, ADR-0015) — no shell export needed. `ref:ENV_VAR` and env-only setups remain available for those who want them.

Ollama needs no key; the wizard lists whatever models the local endpoint reports (`:11434/v1/models`), falling back to manual entry when nothing is running.

## File locations & precedence

Loaded and merged low → high:

1. **Built-in defaults** — compiled into `internal/config`.
2. **Global** — `~/.config/golem/config.toml` (respects `XDG_CONFIG_HOME`).
3. **Project** — `<cwd>/.golem.toml` where the server starts (committable, shareable).
4. **Environment** — `GOLEM_*` variables (e.g. `GOLEM_PROVIDER=zai`).
5. **CLI flags** — highest.

Merge rules: tables (maps) deep-merge; arrays (slices) are **replaced**, not concatenated. First file that fails to parse aborts startup with the file path in the error.

## Secrets

API keys live in the global config by default (ADR-0015): the wizard writes the validated key as plaintext into `~/.config/golem/config.toml` (mode 0600) — the same shape `gh` and the `aws` CLI use for local credentials. Resolution order per provider:

1. `api_key = "sk-..."` in config — plaintext, the designed default for the global file.
2. `api_key = "ref:SOME_ENV_VAR"` in config — env indirection for users who prefer it; a missing referenced var is a startup warning naming the key and turns fail per turn.
3. Dedicated env var (`ANTHROPIC_API_KEY`, `OPENAI_API_KEY`, `ZAI_API_KEY` — both zai plans read the same console key) when the config sets none.

Do not sync `~/.config/golem/` through dotfiles repos or cloud sync. The future project-level `.golem.toml` (committable) will reject plaintext keys.

## Schema

### `[provider]`

| Key | Type | Default | Description |
|---|---|---|---|
| `default` | string | `"anthropic"` | Provider id used when a session doesn't specify |

### `[provider.<id>]` — `id` ∈ `anthropic`, `openai`, `zai`, `zai-coding`, `ollama`

| Key | Type | Default | Description |
|---|---|---|---|
| `model` | string | per-provider | e.g. `claude-opus-5`, `glm-5.3`, `gpt-5.2` |
| `api_key` | string | — | plaintext (default) or `ref:ENV_VAR` (see Secrets) |
| `base_url` | string | provider default | Endpoint override. `zai` preset: `https://api.z.ai/api/paas/v4/` (pay-per-use); `zai-coding` preset: `https://api.z.ai/api/coding/paas/v4/` — GLM Coding Plan keys only work on the coding endpoints |

### `[server]`

| Key | Type | Default | Description |
|---|---|---|---|
| `socket` | string | `$XDG_RUNTIME_DIR/golem.sock` or `~/.local/share/golem/golem.sock` | Unix socket path |
| `tcp_addr` | string | — (disabled) | Optional TCP listener `host:port` for non-local-unix clients |
| `mem_limit_mb` | int | `256` | Sets `GOMEMLIMIT` |
| `session_cache` | int | `8` | Most-recently-active sessions held in memory |

### `[tools]`

| Key | Type | Default | Description |
|---|---|---|---|
| `bash_timeout_secs` | int | `120` | Per-command wall clock limit |
| `output_budget_bytes` | int | `16384` | Per-tool-result byte budget entering model context (truncation marked) |

### `[plugins.mcp.<name>]`

| Key | Type | Description |
|---|---|---|
| `command` | []string | argv to launch the MCP server (required) |
| `env` | map[string]string | Extra env; values support `ref:ENV_VAR` |
| `disabled` | bool | Skip without deleting config |
| `timeout_secs` | int | Per-tool-call timeout (default `30`) |

### Example

```toml
[provider]
default = "zai"

[provider.anthropic]
model = "claude-opus-5"

[provider.zai]
model = "glm-5"
base_url = "https://api.z.ai/api/paas/v4/"
api_key = "ref:ZAI_API_KEY"

[server]
mem_limit_mb = 256

[plugins.mcp.github]
command = ["./github-mcp", "-stdio"]
env = { GITHUB_TOKEN = "ref:GITHUB_TOKEN" }
```

## Plugin lifecycle (MCP)

State machine managed by `internal/mcp`:

```
declared ──(lazy: first session use, or server start if warm=true)
   │ start process
   ▼
starting ──(initialize handshake, tools/list)──► ready
                                                    │ tools registered as mcp_<name>_<tool>
                                                    ▼
                                                 running ──(process exit)──► failed
                                                    │                        │ backoff 1s,2s,4s (max 3)
                                                    │                        ▼
                                                    │                   failed_permanent
                                                    │                   (tools return error results)
                                    server stop / config disable
                                                    ▼
                                                 stopped (SIGTERM, 5s → SIGKILL)
```

Rules:

- **Lazy start** by default; a declared-but-unused plugin costs nothing.
- Registration: MCP tool JSON Schemas pass through to providers unmodified (no translation layer).
- MCP tools execute autonomously like built-in tools; their invocations are observable via `tool_call_start`/`tool_call_end` events.
- A `failed_permanent` plugin's tools return `{"error": "plugin down"}` results to the model rather than blocking the turn.
- Config reload does not restart healthy plugins; only `disabled` toggles and command changes take effect on reload.

## Runtime overrides

- `/model <provider>` in chat/TUI switches provider for the session (in-memory only, not persisted to config).
- `GOLEM_PROVIDER`, `GOLEM_MODEL` env vars override `provider.default`/model for one launch.
- `GOLEM_DISABLE_UPDATE_CHECK=1` silences the once-a-day newer-release notice (checked at launch; the last check time lives in the data directory, and failed checks stay silent).
