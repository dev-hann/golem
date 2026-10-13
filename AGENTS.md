# AGENTS.md — Rules for AI Coding Agents

This file is the entry point for any AI agent (Claude Code, opencode, Cursor, golem itself, etc.) working on this repository. Human contributors: see [CONTRIBUTING.md](CONTRIBUTING.md).

## Golden rules

1. **Read before you write.** Before touching code, read the relevant doc:
   - Structure & boundaries → [docs/ARCHITECTURE.md](docs/ARCHITECTURE.md)
   - Wire format changes → [docs/PROTOCOL.md](docs/PROTOCOL.md)
   - "Why is it like this?" → [docs/DECISIONS.md](docs/DECISIONS.md)
   - "What am I building?" → [docs/ROADMAP.md](docs/ROADMAP.md)
   - Config & plugins → [docs/CONFIG.md](docs/CONFIG.md)
2. **Docs are part of the code.** If your change alters behavior that a document describes, update the document in the same commit. A change with stale docs is an incomplete change.
3. **Never re-litigate settled decisions.** If you disagree with an ADR in docs/DECISIONS.md, propose a new ADR — do not silently violate the old one.
4. **New architectural decisions require a new ADR** appended to docs/DECISIONS.md.

## Development workflow — strict TDD

Every unit of work follows red-green-refactor:

```
1. Pick a task from docs/ROADMAP.md. Locate its test list.
2. RED:   write failing tests first. Table-driven where inputs vary.
3. GREEN: write the minimum implementation to pass.
4. REFACTOR: clean up while tests stay green.
5. Run make all. All of lint + test-race + build must pass.
```

- A task is **done** only when: new tests pass, `make all` passes, and docs are updated if behavior changed.
- Never delete or weaken a test to make it pass. If a test is wrong, explain why and fix the test itself.
- Bug fixes: first write a test that reproduces the bug, then fix it.

## Commands

```bash
make all      # lint + test-race + build — the gate; run before claiming done
make lint     # golangci-lint (strict; config in .golangci.yml)
make test     # go test ./...
make test-race
make cover    # race + coverage; floor is 80% (enforced in CI)
make fmt      # gofumpt + goimports (local prefix github.com/dev-hann/golem)
make build
```

## Code rules

Enforced by lint (`make lint`), but know them:

- Formatting: `gofumpt -extra` + `goimports` with local prefix `github.com/dev-hann/golem`.
- Max line length 120. Functions ≤ 80 lines. Cyclomatic complexity ≤ 15. Nesting depth ≤ 6.
- Errors: always check, always wrap with context (`fmt.Errorf("reading session %s: %w", id, err)`). Use `errors.Is/As`, never string-match errors.
- Every `switch` over an enum must be exhaustive (or explicitly `default` with rationale).
- Exported symbols require doc comments starting with the symbol name.
- No magic numbers/strings — named constants.
- Tables in tests: name every case, assert both success and error paths.
- `t.Parallel()` in every test that can run in parallel. Use `t.Cleanup` for teardown.
- No `init()`. No global mutable state. Dependencies are injected via constructors (`New*` returning `(*T, error)`).
- Context is the first parameter, named `ctx`, never stored on structs.
- Timeouts and cancellations: every network/exec call takes a context with deadline.

## Boundaries (violations will be rejected)

```
cmd/*           → may import internal/* and pkg/*
pkg/golemclient → may import nothing from internal/* (public client library)
internal/*      → may import internal/* and pkg/*; NEVER cmd/*
apps/*          → future web/desktop clients; talk to the server via pkg/golemclient or the protocol only
```

- `internal/agent` owns the agent loop. Tools do not call the LLM. The LLM layer does not know tools exist.
- Protocol types live in `pkg/golemclient` (shared contract), not in `internal/server`.
- No new dependency without an ADR-level justification (prefer stdlib; current allowed set is in docs/ARCHITECTURE.md).

## Commits

- Conventional Commits: `feat:`, `fix:`, `docs:`, `refactor:`, `test:`, `chore:`, `perf:`.
- One logical change per commit. Tests + implementation + doc updates for one change belong in the same commit.
- Never commit secrets, API keys, or `.env` files.

## When stuck or uncertain

- Ambiguity in protocol/config: the doc is the source of truth; if the doc is ambiguous, fix the doc first (small PR), then proceed.
- Do not invent new event types, config keys, or CLI flags without adding them to the relevant doc in the same change.
