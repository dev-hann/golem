# Contributing to golem

golem is developed test-first with strict uniformity gates. Everything below is enforced in CI; run `make all` locally before opening a PR.

## Setup

```bash
go version          # 1.25+
make build          # compiles server + TUI
golangci-lint version   # install: https://golangci-lint.run/welcome/install/
```

## The gate

A PR is mergeable only when **all** of the following hold:

1. `make lint` passes — strict golangci-lint config (`.golangci.yml`): gofumpt formatting, 120-char lines, ≤80-line functions, complexity ≤15, exhaustive switches, error wrapping, security checks.
2. `go test -race ./...` passes — no data races.
3. Total coverage ≥ 80% (`make cover`).
4. `go build ./...` and `go vet ./...` pass.
5. Tests were written **before** the implementation for new behavior (TDD; see AGENTS.md workflow).
6. Documentation updated in the same PR whenever behavior, protocol, or config changes. Docs live in `docs/` and are the source of truth — see the per-doc responsibility table below.

| If you changed… | You must update |
|---|---|
| Event types / wire format | docs/PROTOCOL.md |
| Module structure / boundaries | docs/ARCHITECTURE.md |
| Config keys / plugin behavior | docs/CONFIG.md |
| An architectural decision | docs/DECISIONS.md (new ADR) |
| Nothing user-visible | No doc change needed |

## Style rules that lint cannot check

- Errors are wrapped with context and checked immediately; no discarded errors.
- Table-driven tests with named cases, covering error paths, `t.Parallel()` where possible.
- No `init()`, no global mutable state; constructor injection everywhere.
- New dependencies require an ADR in docs/DECISIONS.md explaining why stdlib or existing deps can't do it.

## Driving the TUI (E2E checks)

Manual, not part of `make all`. Two canonical tools — pick by what you need to see:

| Tool | Captures | Use for |
|---|---|---|
| `scripts/tui-drive.py` | Raw pty byte stream (`pty.fork`) | Repeatable scenarios: key handling, exit ladders, protocol-driven flow |
| tmux `send-keys` + `capture-pane` | The **rendered screen** | Visual truth: viewport, scrollback, layout — what the user actually sees |

### tui-drive.py (pty driver) — the default

For pty-level checks of the TUI, use the unified runner instead of writing ad-hoc pty scripts:

```bash
printf 'sleep 3\nkeys \x03\n' | scripts/tui-drive.py   # boot, ctrl+c once, dump the screen
```

The runner bakes in isolation and cleanup: a fresh temp dir per run (`XDG_DATA_HOME`/`XDG_RUNTIME_DIR`), a fresh `go build` into it, SIGTERM of the spawned `golem serve` via the isolated pidfile, and temp-dir removal. This is load-bearing: the launcher restarts any server whose version differs (`internal/cli/launch.go`), so an ad-hoc script that runs a second build against the default socket will SIGTERM the production server and EOF every live TUI session — exactly the incident this runner replaced. Known fidelity artifact: under a driven pty the ctrl+c exit ladder absorbs one extra press (see the runner's docstring).

### tmux — when you need the rendered screen

The pty runner captures bytes, not screens. When the question is "what does the viewport actually look like" (scroll position, layout, a repaint), run the TUI inside a tmux pane and screenshot it:

```bash
D=.wt/tmux; mkdir -p "$D/data" "$D/run" && make build
tmux new-session -d -s gt -x 100 -y 30 \
  "env XDG_DATA_HOME=$PWD/$D/data XDG_RUNTIME_DIR=$PWD/$D/run ./bin/golem 2>$D/err.log; sleep 600"
tmux send-keys -t gt C-g      # drive any key: C-x l, Enter, C-g, …
tmux capture-pane -t gt -p    # prints the rendered viewport
tmux kill-session -t gt; kill "$(cat "$D/run/golem.sock.pid")" 2>/dev/null
```

The same isolation contract applies: `XDG_DATA_HOME`/`XDG_RUNTIME_DIR` must point away from the production socket (see the warning above), and the detached `serve` the TUI spawned must be stopped via its isolated pidfile before you delete the dir. Throwaway helper scripts built on this recipe (screenshot ladders, scroll probes) are scaffolding — don't commit them; rebuild from this recipe when needed.

## Commits

Conventional Commits (`feat:`, `fix:`, `docs:`, `refactor:`, `test:`, `chore:`, `perf:`). One logical change per commit, including its tests and doc updates.

## Reporting bugs

Open an issue with: what you ran, what you expected, what happened, and the session JSONL if relevant (redact secrets first). For security vulnerabilities, see [SECURITY.md](SECURITY.md) — do not open public issues for those.
