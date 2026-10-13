package cli_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/dev-hann/golem/internal/cli"
)

// writeProviderConfig seeds a config file and returns its path.
func writeProviderConfig(t *testing.T, body string) string {
	t.Helper()

	path := filepath.Join(t.TempDir(), "config.toml")
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}

	return path
}

// clearProviderKeys empties every provider key env var.
func clearProviderKeys(t *testing.T) {
	t.Helper()

	for _, key := range []string{"ZAI_API_KEY", "OPENAI_API_KEY", "ANTHROPIC_API_KEY", "GOLEM_PROVIDER", "GOLEM_MODEL"} {
		t.Setenv(key, "")
	}
}

func TestResolveProvider_NoConfigInfersSingleEnvKey(t *testing.T) {
	clearProviderKeys(t)
	t.Setenv("ZAI_API_KEY", "sk-zai")

	choice, err := cli.ResolveProvider(filepath.Join(t.TempDir(), "absent.toml"), nil)
	if err != nil {
		t.Fatalf("ResolveProvider: %v", err)
	}

	if choice.ID != "zai" {
		t.Errorf("ID = %q, want zai", choice.ID)
	}
	if choice.Model != "glm-5" {
		t.Errorf("Model = %q, want preset default glm-5", choice.Model)
	}
	if choice.Provider == nil {
		t.Error("Provider is nil")
	}
}

func TestResolveProvider_NoConfigNoKeysFallsBackToAnthropic(t *testing.T) {
	clearProviderKeys(t)

	choice, err := cli.ResolveProvider(filepath.Join(t.TempDir(), "absent.toml"), nil)
	if err != nil {
		t.Fatalf("ResolveProvider: %v", err)
	}

	if choice.ID != "anthropic" || choice.Model != "claude-opus-5" {
		t.Errorf("choice = %+v, want anthropic/claude-opus-5", choice)
	}
}

func TestResolveProvider_TwoKeysWithoutConfigKeepsDefault(t *testing.T) {
	clearProviderKeys(t)
	t.Setenv("ZAI_API_KEY", "sk-1")
	t.Setenv("OPENAI_API_KEY", "sk-2")

	choice, err := cli.ResolveProvider(filepath.Join(t.TempDir(), "absent.toml"), nil)
	if err != nil {
		t.Fatalf("ResolveProvider: %v", err)
	}

	if choice.ID != "anthropic" {
		t.Errorf("ID = %q, want anthropic (ambiguous inference skipped)", choice.ID)
	}
}

func TestResolveProvider_ConfigFileWins(t *testing.T) {
	clearProviderKeys(t)
	t.Setenv("OPENAI_API_KEY", "sk-openai")

	path := writeProviderConfig(t, `
[provider]
default = "ollama"

[provider.ollama]
model = "llama3.3"
`)

	choice, err := cli.ResolveProvider(path, nil)
	if err != nil {
		t.Fatalf("ResolveProvider: %v", err)
	}

	if choice.ID != "ollama" || choice.Model != "llama3.3" {
		t.Errorf("choice = %+v, want ollama/llama3.3", choice)
	}
}

func TestResolveProvider_ModelPrecedence(t *testing.T) {
	clearProviderKeys(t)
	t.Setenv("ZAI_API_KEY", "sk-zai")

	path := writeProviderConfig(t, `
[provider]
default = "zai"

[provider.zai]
model = "glm-4.7"
`)

	choice, err := cli.ResolveProvider(path, nil)
	if err != nil {
		t.Fatalf("ResolveProvider: %v", err)
	}
	if choice.Model != "glm-4.7" {
		t.Errorf("Model = %q, want file value glm-4.7", choice.Model)
	}

	t.Setenv("GOLEM_MODEL", "glm-5.3")

	choice, err = cli.ResolveProvider(path, nil)
	if err != nil {
		t.Fatalf("ResolveProvider: %v", err)
	}
	if choice.Model != "glm-5.3" {
		t.Errorf("Model = %q, want env override glm-5.3", choice.Model)
	}
}

func TestResolveProvider_BrokenRefFailsNamingVar(t *testing.T) {
	clearProviderKeys(t)

	path := writeProviderConfig(t, `
[provider]
default = "zai"

[provider.zai]
model = "glm-5"
api_key = "ref:GOLEM_MISSING_VAR"
`)

	_, err := cli.ResolveProvider(path, nil)
	if err == nil || !strings.Contains(err.Error(), "GOLEM_MISSING_VAR") {
		t.Errorf("expected error naming GOLEM_MISSING_VAR, got: %v", err)
	}
}

func TestResolveProvider_ModellessProviderRequiresModel(t *testing.T) {
	clearProviderKeys(t)

	path := writeProviderConfig(t, `
[provider]
default = "ollama"
`)

	_, err := cli.ResolveProvider(path, nil)
	if err == nil || !strings.Contains(err.Error(), "model") {
		t.Errorf("expected model-required error, got: %v", err)
	}
}

func TestResolveProvider_KeyFallsBackToPresetEnv(t *testing.T) {
	clearProviderKeys(t)
	t.Setenv("ZAI_API_KEY", "sk-from-env")

	path := writeProviderConfig(t, `
[provider]
default = "zai"
`)

	choice, err := cli.ResolveProvider(path, nil)
	if err != nil {
		t.Fatalf("ResolveProvider: %v", err)
	}
	if choice.ID != "zai" {
		t.Errorf("ID = %q, want zai", choice.ID)
	}
}
