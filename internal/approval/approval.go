// Package approval decides whether a shell command may run without human
// confirmation. Non-interactive callers (CLI, tests) treat "would prompt" as
// deny; the interactive gate arrives with the server/TUI phases.
package approval

import (
	"strings"
)

// Mode selects how non-whitelisted commands are handled.
type Mode string

// Approval modes. See docs/CONFIG.md ([approval].mode).
const (
	// ModePrompt requires confirmation for anything not whitelisted.
	ModePrompt Mode = "prompt"
	// ModeAllAuto allows everything without prompting. Dangerous.
	ModeAllAuto Mode = "all_auto"
)

// Decision is the outcome of a policy check.
type Decision int

// Policy decisions.
const (
	// DecisionAllow means the command may run.
	DecisionAllow Decision = iota
	// DecisionDeny means the command must not run without confirmation.
	DecisionDeny
)

// DefaultPrefixes is the out-of-the-box read-only whitelist from
// docs/CONFIG.md.
var DefaultPrefixes = []string{
	"ls", "cat", "head", "tail",
	"git status", "git log", "git diff",
	"grep", "rg", "find", "wc",
}

// shellSeparators splits compound commands into independently-checked
// segments.
var shellSeparators = strings.NewReplacer(
	"&&", "\x00", "||", "\x00", "|", "\x00", ";", "\x00", "\n", "\x00",
)

// dangerSubstrings force a deny regardless of prefixes: command substitution
// and output redirection can smuggle side effects past a prefix check.
var dangerSubstrings = []string{"$(", "`", ">", "<"}

// Policy is an immutable approval policy.
type Policy struct {
	mode      Mode
	prefixes  [][]string
	allowAll  bool
	denyChars []string
}

// NewPolicy builds a policy from a mode and whitelist prefixes. Prefixes
// match whole words at the start of each command segment ("git status"
// matches "git status -s" but not "git statusx").
func NewPolicy(mode Mode, autoPrefixes []string) *Policy {
	parsed := make([][]string, 0, len(autoPrefixes))
	for _, p := range autoPrefixes {
		if words := strings.Fields(p); len(words) > 0 {
			parsed = append(parsed, words)
		}
	}

	return &Policy{
		mode:      mode,
		prefixes:  parsed,
		allowAll:  mode == ModeAllAuto,
		denyChars: dangerSubstrings,
	}
}

// Mode reports the configured mode.
func (p *Policy) Mode() Mode { return p.mode }

// Check evaluates a full command line. Compound commands (chained with
// &&, ||, |, ;, or newlines) require every segment to be whitelisted.
func (p *Policy) Check(command string) Decision {
	if p.allowAll {
		return DecisionAllow
	}

	for _, danger := range p.denyChars {
		if strings.Contains(command, danger) {
			return DecisionDeny
		}
	}

	for _, segment := range strings.Split(shellSeparators.Replace(command), "\x00") {
		if segmentAllowed(segment, p.prefixes) == DecisionDeny {
			return DecisionDeny
		}
	}

	return DecisionAllow
}

// segmentAllowed reports whether one command segment starts with a
// whitelisted word-boundary prefix.
func segmentAllowed(segment string, prefixes [][]string) Decision {
	words := strings.Fields(segment)
	if len(words) == 0 {
		return DecisionAllow
	}

	for _, prefix := range prefixes {
		if wordsMatchPrefix(words, prefix) {
			return DecisionAllow
		}
	}

	return DecisionDeny
}

// wordsMatchPrefix reports whether the first len(prefix) command words equal
// the prefix words exactly — word boundaries, no partial matches.
func wordsMatchPrefix(words, prefix []string) bool {
	if len(words) < len(prefix) {
		return false
	}

	for i, want := range prefix {
		if words[i] != want {
			return false
		}
	}

	return true
}
