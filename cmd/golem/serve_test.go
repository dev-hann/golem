package main

import (
	"os"
	"path/filepath"
	"testing"
)

func TestRun_ChatUnreachableSocketExitsOne(t *testing.T) {
	t.Setenv("XDG_RUNTIME_DIR", t.TempDir())
	if got := run([]string{"chat"}, defaultDeps()); got != 1 {
		t.Errorf("run(chat) with no server = %d, want 1", got)
	}
}

func TestRun_ServeBlockedSocketExitsOne(t *testing.T) {
	runtimeDir := t.TempDir()
	t.Setenv("XDG_RUNTIME_DIR", runtimeDir)
	blocked, err := os.Create(filepath.Join(runtimeDir, "golem.sock"))
	if err != nil {
		t.Fatalf("create socket file: %v", err)
	}
	defer blocked.Close()
	if got := run([]string{"serve"}, defaultDeps()); got != 1 {
		t.Errorf("run(serve) with occupied socket = %d, want 1", got)
	}
}

func TestRun_UnknownSubcommandExitsTwo(t *testing.T) {
	if got := run([]string{"teleport"}, defaultDeps()); got != 2 {
		t.Errorf("run(teleport) = %d, want 2", got)
	}
}
