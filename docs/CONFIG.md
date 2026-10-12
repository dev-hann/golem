# golem Configuration & Plugin Lifecycle

Last updated: 2026-09-27. Schema changes require updating this file in the same commit. Parsed by `internal/config` (`BurntSushi/toml`, ADR-0012).

## File locations & precedence

Loaded and merged low → high:

1. **Built-in defaults** — compiled into `internal/config`.
2. **Global** — `~/.config/golem/config.toml` (respects `XDG_CONFIG_HOME`).
3. **Project** — `<cwd>/.golem.toml` where the server starts (committable, shareable).
4. **Environment** — `GOLEM_*` variables (e.g. `GOLEM_PROVIDER=zai`).
5. **CLI flags** — highest.

Merge rules: tables (maps) deep-merge; arrays (slices) are **replaced**, not concatenated. First file that fails to parse aborts startup with the file path in the error.

## Secrets

API keys are never stored in config files. Resolution order per provider:

1. Dedicated env var (`ANTHROPIC_API_KEY`, `OPENAI_API_KEY`, `ZAI_API_KEY`).
2. `api_key = "ref:SOME_ENV_VAR"` in config — any env var, resolved at startup. Missing referenced var is a startup error naming the key.

Plaintext `api_key = "sk-..."` is accepted with a stderr warning, for testing only.

## Schema

### `[provider]`

| Key | Type | Default | Description |
|---|---|---|---|
| `default` | string | `"anthropic"` | Provider id used when a session doesn't specify |

### `[provider.<id>]` — `id` ∈ `anthropic`, `openai`, `zai`, `ollama`

| Key | Type | Default | Description |
|---|---|---|---|
| `model` | string | per-provider | e.g. `claude-opus-5`, `glm-5`, `gpt-5.2` |
| `api_key` | string | — | `ref:ENV_VAR` indirection (see Secrets) |
| `base_url` | string | provider default | Endpoint override. `zai` preset: `https://api.z.ai/api/paas/v4/` |

### `[server]`

| Key | Type | Default | Description |
|---|---|---|---|
| `socket` | string | `$XDG_RUNTIME_DIR/golem.sock` or `~/.local/share/golem/golem.sock` | Unix socket path |
| `tcp_addr` | string | — (disabled) | Optional TCP listener `host:port` for non-local-unix clients |
| `mem_limit_mb` | int | `256` | Sets `GOMEMLIMIT` |
| `session_cache` | int | `8` | Most-recently-active sessions held in memory |

### `[approval]`

| Key | Type | Default | Description |
|---|---|---|---|
| `mode` | string | `"prompt"` | `prompt` = gate non-whitelisted; `all_auto` = never block (dangerous, warns at startup) |
| `auto_prefixes` | []string | `["ls", "cat", "head", "tail", "git status", "git log", "git diff", "grep", "rg", "find", "wc"]` | Word-boundary prefix match against the command line |
| `timeout_secs` | int | `120` | Approval wait; expiry = deny |

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

[approval]
auto_prefixes = ["ls", "cat", "git status", "git diff", "rg"]

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
- MCP tools are subject to the same approval gate as built-in tools; tools that declare no side effects still default to requiring approval (opt-out per plugin is a non-goal for now).
- A `failed_permanent` plugin's tools return `{"error": "plugin down"}` results to the model rather than blocking the turn.
- Config reload does not restart healthy plugins; only `disabled` toggles and command changes take effect on reload.

## Runtime overrides

- `/model <provider>` in chat/TUI switches provider for the session (in-memory only, not persisted to config).
- `GOLEM_PROVIDER`, `GOLEM_MODEL` env vars override `provider.default`/model for one launch.
