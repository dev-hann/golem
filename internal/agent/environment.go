package agent

import (
	"fmt"
	"runtime"
	"strings"
	"time"
)

// EnvironmentContext renders the environment block injected between the
// system prompt and AGENTS.md instructions on every turn (ADR-0031):
// the workspace path, the platform, and today's date. cwd may be empty
// (the line is omitted); now must be the caller's current time.
func EnvironmentContext(cwd string, now time.Time) string {
	var b strings.Builder

	if cwd != "" {
		fmt.Fprintf(&b, "Working directory: %s (paths are workspace-relative)\n", cwd)
	}

	fmt.Fprintf(&b, "Platform: %s\n", runtime.GOOS)
	fmt.Fprintf(&b, "Today: %s", now.Format("2006-01-02"))

	return b.String()
}
