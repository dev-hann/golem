package llm_test

import (
	"testing"

	"github.com/dev-hann/golem/internal/llm"
)

func TestPresets_CoverKnownProviders(t *testing.T) {
	t.Parallel()

	tests := []struct {
		id        string
		kind      llm.Kind
		keyEnv    string
		baseURL   string
		wantModel string
	}{
		{id: "anthropic", kind: llm.KindAnthropic, keyEnv: "ANTHROPIC_API_KEY", baseURL: "", wantModel: "claude-opus-5"},
		{id: "openai", kind: llm.KindOpenAICompat, keyEnv: "OPENAI_API_KEY", baseURL: "", wantModel: "gpt-5.2"},
		{id: "zai", kind: llm.KindOpenAICompat, keyEnv: "ZAI_API_KEY", baseURL: "https://api.z.ai/api/paas/v4/", wantModel: "glm-5"},
		{id: "ollama", kind: llm.KindOpenAICompat, keyEnv: "", baseURL: "http://localhost:11434/v1", wantModel: ""},
	}
	for _, tt := range tests {
		t.Run(tt.id, func(t *testing.T) {
			t.Parallel()

			preset, ok := llm.Presets[tt.id]
			if !ok {
				t.Fatalf("preset %q missing", tt.id)
			}

			if preset.Kind != tt.kind {
				t.Errorf("Kind = %q, want %q", preset.Kind, tt.kind)
			}
			if preset.KeyEnv != tt.keyEnv {
				t.Errorf("KeyEnv = %q, want %q", preset.KeyEnv, tt.keyEnv)
			}
			if preset.BaseURL != tt.baseURL {
				t.Errorf("BaseURL = %q, want %q", preset.BaseURL, tt.baseURL)
			}
			if preset.DefaultModel != tt.wantModel {
				t.Errorf("DefaultModel = %q, want %q", preset.DefaultModel, tt.wantModel)
			}
		})
	}
}

func TestPresets_OrderListsAllIDs(t *testing.T) {
	t.Parallel()

	if len(llm.Order) != len(llm.Presets) {
		t.Errorf("Order has %d entries, Presets has %d", len(llm.Order), len(llm.Presets))
	}

	seen := map[string]bool{}
	for _, id := range llm.Order {
		if seen[id] {
			t.Errorf("duplicate id %q in Order", id)
		}
		if _, ok := llm.Presets[id]; !ok {
			t.Errorf("Order references unknown preset %q", id)
		}

		seen[id] = true
	}
}

func TestDetectFromEnv_ListsProvidersWithKeys(t *testing.T) {
	t.Setenv("ZAI_API_KEY", "sk-zai")
	t.Setenv("OPENAI_API_KEY", "")
	t.Setenv("ANTHROPIC_API_KEY", "")
	t.Setenv("GOLEM_TEST_UNRELATED", "x")

	detected := llm.DetectFromEnv()
	if len(detected) != 1 || detected[0] != "zai" {
		t.Errorf("DetectFromEnv() = %v, want [zai]", detected)
	}
}

func TestDetectFromEnv_None(t *testing.T) {
	t.Setenv("ZAI_API_KEY", "")
	t.Setenv("OPENAI_API_KEY", "")
	t.Setenv("ANTHROPIC_API_KEY", "")

	if got := llm.DetectFromEnv(); len(got) != 0 {
		t.Errorf("DetectFromEnv() = %v, want empty", got)
	}
}
