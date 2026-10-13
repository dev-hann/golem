# Security Policy

golem executes shell commands requested by a model and (once phase 6 lands)
manages MCP child processes. Treat any input that reaches those paths as
untrusted.

## Reporting a vulnerability

Do **not** open a public issue for security problems. Use GitHub's private
vulnerability reporting:

<https://github.com/dev-hann/golem/security/advisories/new>

Include: the commit you tested, steps to reproduce, and impact. Reports are
acknowledged within 7 days.

## Scope

In scope:

- The approval gate (`internal/approval`) — bypasses of the prefix whitelist,
  the compound-command segmentation, or the substitution/redirection deny rule.
- Shell execution (`internal/tools` bash) and tool output handling.
- The server transport and protocol (`internal/server`, `pkg/golemclient`) as
  reachable from the unix socket / future TCP listener.
- Secret handling (env vars today; `ref:` config indirection in phase 5).

Out of scope:

- Compromised LLM providers returning malicious content (accepted risk of the
  category).
- Anything requiring local code execution privileges already.

## Supported versions

Pre-release: only the `main` branch receives fixes.
