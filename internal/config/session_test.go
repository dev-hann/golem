package config_test

import (
	"testing"

	"github.com/dev-hann/golem/internal/config"
)

// ADR-0025: [session] auto_compact_bytes configures the server's
// automatic compaction threshold. Absent means the caller's default;
// zero and negative explicitly disable automatic compaction.

func TestLoad_ParsesSessionAutoCompactBytes(t *testing.T) {
	t.Parallel()

	path := writeConfig(t, `
[session]
auto_compact_bytes = 262144
`)

	cfg, err := config.Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}

	if cfg.Session.AutoCompactBytes != 262144 {
		t.Errorf("AutoCompactBytes = %d, want 262144", cfg.Session.AutoCompactBytes)
	}
}

func TestLoad_SessionTableOptional(t *testing.T) {
	t.Parallel()

	path := writeConfig(t, `
[provider]
default = "zai"
`)

	cfg, err := config.Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}

	if cfg.Session.AutoCompactBytes != 0 {
		t.Errorf("AutoCompactBytes = %d, want 0 when [session] is absent", cfg.Session.AutoCompactBytes)
	}
}

func TestLoad_ZeroAutoCompactBytesDisables(t *testing.T) {
	t.Parallel()

	path := writeConfig(t, `
[session]
auto_compact_bytes = 0
`)

	cfg, err := config.Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}

	if cfg.Session.AutoCompactBytes != 0 {
		t.Errorf("AutoCompactBytes = %d, want 0 (explicit disable)", cfg.Session.AutoCompactBytes)
	}
}

func TestLoad_NegativeAutoCompactBytesIgnored(t *testing.T) {
	t.Parallel()

	path := writeConfig(t, `
[session]
auto_compact_bytes = -5
`)

	cfg, err := config.Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}

	if cfg.Session.AutoCompactBytes != 0 {
		t.Errorf("AutoCompactBytes = %d, want 0 (negative rejected)", cfg.Session.AutoCompactBytes)
	}
}
