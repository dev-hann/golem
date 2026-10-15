package agent

import (
	"os"
	"path/filepath"
	"strings"

	"github.com/dev-hann/golem/internal/tools"
)

// InstructionsMaxBytes caps one discovered instruction file (AGENTS.md)
// before it enters the system prompt. Oversized files are clipped with a
// truncation marker so the model knows context was dropped.
const InstructionsMaxBytes = 32 * 1024

// DiscoverInstructions reads instruction files for a workspace: the
// user-level ~/.config/golem/AGENTS.md first, then the project-local
// <cwd>/AGENTS.md — the nearer file comes later, so it reads as the more
// specific override (ADR-0020). Missing or blank files are skipped; each
// file is clipped at InstructionsMaxBytes. It returns "" when nothing is
// discovered.
func DiscoverInstructions(cwd string) string {
	var b strings.Builder

	global := globalInstructionsPath()
	for _, path := range []string{global, filepath.Join(cwd, "AGENTS.md")} {
		if appendInstruction(&b, path) {
			b.WriteString("\n\n")
		}
	}

	return strings.TrimSuffix(b.String(), "\n\n")
}

// appendInstruction reads one candidate file, appends its header and
// (possibly clipped) contents to b, and reports whether anything was
// appended. Blank or unreadable files append nothing.
func appendInstruction(b *strings.Builder, path string) bool {
	if path == "" {
		return false
	}

	data, err := os.ReadFile(path) //nolint:gosec // path is fixed or workspace-local
	if err != nil {
		return false // a missing instructions file is not an error
	}

	text := strings.TrimSpace(string(data))
	if text == "" {
		return false
	}

	clipped, truncated := tools.Truncate(text, InstructionsMaxBytes)

	b.WriteString("\n\n")
	b.WriteString(strings.TrimSpace(
		strings.ReplaceAll(filepath.ToSlash(path), "\n", " "),
	))
	b.WriteString(":\n")
	b.WriteString(clipped)

	if truncated {
		b.WriteString("\n...[truncated]")
	}

	return true
}

// globalInstructionsPath resolves ~/.config/golem/AGENTS.md through the
// same XDG_CONFIG_HOME precedence as the config file (docs/CONFIG.md);
// "" when the user home is unresolvable.
func globalInstructionsPath() string {
	if dir := os.Getenv("XDG_CONFIG_HOME"); dir != "" {
		return filepath.Join(dir, "golem", "AGENTS.md")
	}

	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}

	return filepath.Join(home, ".config", "golem", "AGENTS.md")
}
