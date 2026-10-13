package config_test

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/dev-hann/golem/internal/config"
)

func writeConfig(t *testing.T, body string) string {
	t.Helper()

	path := filepath.Join(t.TempDir(), "config.toml")
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}

	return path
}

func TestLoad_MissingFileAppliesDefaults(t *testing.T) {
	t.Parallel()

	cfg, err := config.Load(filepath.Join(t.TempDir(), "absent.toml"))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}

	if cfg.Default != "anthropic" {
		t.Errorf("Default = %q, want anthropic", cfg.Default)
	}
	if len(cfg.Providers) != 0 {
		t.Errorf("Providers = %v, want empty", cfg.Providers)
	}
	if cfg.Path != "" {
		t.Errorf("Path = %q, want empty when no file was read", cfg.Path)
	}
}

func TestLoad_ParsesProviderSections(t *testing.T) {
	t.Parallel()

	path := writeConfig(t, `
[provider]
default = "zai"

[provider.zai]
model = "glm-5"
base_url = "https://api.z.ai/api/paas/v4/"
api_key = "ref:ZAI_API_KEY"

[provider.ollama]
base_url = "http://localhost:11434/v1"
`)

	cfg, err := config.Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}

	if cfg.Default != "zai" {
		t.Errorf("Default = %q, want zai", cfg.Default)
	}
	if cfg.Path != path {
		t.Errorf("Path = %q, want %q", cfg.Path, path)
	}

	zai := cfg.Providers["zai"]
	if zai.Model != "glm-5" || zai.BaseURL != "https://api.z.ai/api/paas/v4/" || zai.APIKey != "ref:ZAI_API_KEY" {
		t.Errorf("zai settings = %+v", zai)
	}
	if ollama := cfg.Providers["ollama"]; ollama.BaseURL != "http://localhost:11434/v1" {
		t.Errorf("ollama settings = %+v", ollama)
	}
}

func TestLoad_ParseErrorNamesFile(t *testing.T) {
	t.Parallel()

	path := writeConfig(t, "[provider\nbroken =")
	_, err := config.Load(path)
	if err == nil || !strings.Contains(err.Error(), path) {
		t.Errorf("expected parse error naming %q, got: %v", path, err)
	}
}

func TestLoad_EnvOverridesDefault(t *testing.T) {
	t.Setenv("GOLEM_PROVIDER", "ollama")
	path := writeConfig(t, "[provider]\ndefault = \"zai\"\n")

	cfg, err := config.Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}

	if cfg.Default != "ollama" {
		t.Errorf("Default = %q, want env override ollama", cfg.Default)
	}
}

func TestResolveKey_RefResolvesFromEnv(t *testing.T) {
	t.Setenv("GOLEM_TEST_KEY", "sk-test")

	key, err := config.ResolveKey("ref:GOLEM_TEST_KEY")
	if err != nil {
		t.Fatalf("ResolveKey: %v", err)
	}
	if key != "sk-test" {
		t.Errorf("key = %q, want sk-test", key)
	}
}

func TestResolveKey_RefMissingEnvFails(t *testing.T) {
	t.Setenv("GOLEM_TEST_KEY", "")

	_, err := config.ResolveKey("ref:GOLEM_TEST_KEY")
	if err == nil || !strings.Contains(err.Error(), "GOLEM_TEST_KEY") {
		t.Errorf("expected error naming GOLEM_TEST_KEY, got: %v", err)
	}
	if !errors.Is(err, config.ErrKeyUnresolved) {
		t.Errorf("error must wrap ErrKeyUnresolved, got: %v", err)
	}
}

func TestResolveKey_PlaintextPasses(t *testing.T) {
	key, err := config.ResolveKey("sk-plaintext")
	if err != nil {
		t.Fatalf("ResolveKey: %v", err)
	}
	if key != "sk-plaintext" {
		t.Errorf("key = %q, want passthrough", key)
	}
}

func TestDefaultPath_RespectsXDGConfigHome(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", "/tmp/xdg-test")
	if got := config.DefaultPath(); got != "/tmp/xdg-test/golem/config.toml" {
		t.Errorf("DefaultPath() = %q", got)
	}
}
