package cli

import (
	"os"
	"path/filepath"
)

// dataDir resolves the golem data directory (sessions, sockets).
func dataDir() string {
	if dir := os.Getenv("XDG_DATA_HOME"); dir != "" {
		return filepath.Join(dir, "golem")
	}

	home, err := os.UserHomeDir()
	if err != nil {
		return ".golem"
	}

	return filepath.Join(home, ".local", "share", "golem")
}

// DefaultSocketPath is the unix socket servers listen on and clients dial.
func DefaultSocketPath() string {
	if dir := os.Getenv("XDG_RUNTIME_DIR"); dir != "" {
		return filepath.Join(dir, "golem.sock")
	}

	return filepath.Join(dataDir(), "golem.sock")
}

// DefaultSessionsDir is where session JSONL files live.
func DefaultSessionsDir() string {
	return filepath.Join(dataDir(), "sessions")
}
