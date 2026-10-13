package cli_test

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/dev-hann/golem/internal/cli"
)

func TestFetchModelsForPicker_ListsCurrentProviderModels(t *testing.T) {
	clearProviderKeys(t)

	base := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", base)

	if err := os.MkdirAll(filepath.Join(base, "golem"), 0o750); err != nil {
		t.Fatalf("mkdir: %v", err)
	}

	cfgPath := filepath.Join(base, "golem", "config.toml")
	body := `
[provider]
default = "zai-coding"

[provider.zai-coding]
api_key = "sk-coding"
`
	if err := os.WriteFile(cfgPath, []byte(body), 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}

	var gotID, gotKey string

	models := cli.FetchModelsForPicker(func(id, key string) ([]string, error) {
		gotID, gotKey = id, key

		return []string{"glm-5.3", "glm-5.3-flash", "embedding-3"}, nil
	})

	if gotID != "zai-coding" || gotKey != "sk-coding" {
		t.Errorf("list called with (%q, %q), want zai-coding/sk-coding", gotID, gotKey)
	}

	// Normalized: non-chat filtered, sorted.
	if len(models) != 2 || models[0] != "glm-5.3" || models[1] != "glm-5.3-flash" {
		t.Errorf("models = %v", models)
	}
}

func TestFetchModelsForPicker_SilentOnFailure(t *testing.T) {
	clearProviderKeys(t)

	t.Setenv("XDG_CONFIG_HOME", t.TempDir())

	if got := cli.FetchModelsForPicker(func(_, _ string) ([]string, error) {
		return nil, errors.New("endpoint down")
	}); got != nil {
		t.Errorf("failing list must degrade to nil, got %v", got)
	}
}
