// Build version reporting: ldflags injection (goreleaser, make build)
// with a buildinfo fallback for `go install pkg@vX.Y.Z` builds.

package main

import (
	"fmt"
	"io"
	"runtime"
	"runtime/debug"
)

// Injected at build time; plain `go build` keeps the dev defaults.
var (
	version = "dev"
	commit  = "none"
)

// resolveVersion prefers the ldflags value, then the module version the
// toolchain embeds for `go install pkg@vX.Y.Z` (ldflags cannot be passed
// there), then dev.
func resolveVersion() string {
	if version != "dev" {
		return version
	}

	if info, ok := debug.ReadBuildInfo(); ok {
		if v := info.Main.Version; v != "" && v != "(devel)" {
			return v
		}
	}

	return version
}

// runVersion prints the build version.
func runVersion(w io.Writer) int {
	fmt.Fprintf(w, "golem %s (commit %s, %s/%s)\n", //nolint:errcheck // terminal output
		resolveVersion(), commit, runtime.GOOS, runtime.GOARCH)

	return 0
}
