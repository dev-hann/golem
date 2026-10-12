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

## Commits

Conventional Commits (`feat:`, `fix:`, `docs:`, `refactor:`, `test:`, `chore:`, `perf:`). One logical change per commit, including its tests and doc updates.

## Reporting bugs

Open an issue with: what you ran, what you expected, what happened, and the session JSONL if relevant (redact secrets first).
