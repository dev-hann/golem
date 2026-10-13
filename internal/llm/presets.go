// Provider presets: the built-in provider catalog. Every id maps to an
// adapter kind, the environment variable that carries its API key, an
// optional endpoint override, and a default model. Configuration merges
// over these (docs/CONFIG.md [provider.<id>]).

package llm

import "os"

// Kind selects which adapter implements a provider.
type Kind string

// Adapter kinds.
const (
	// KindAnthropic is served by internal/llm/anthropic.
	KindAnthropic Kind = "anthropic"
	// KindOpenAICompat is served by internal/llm/openai: any
	// OpenAI-compatible endpoint (openai, zai, ollama, gateways).
	KindOpenAICompat Kind = "openai-compat"
)

// Preset describes one built-in provider.
type Preset struct {
	Kind         Kind
	Label        string
	KeyEnv       string // "" when the provider needs no key (ollama)
	BaseURL      string // "" when the adapter default applies
	DefaultModel string
}

// Presets is the provider catalog keyed by id.
var Presets = map[string]Preset{
	"anthropic": {
		Kind: KindAnthropic, Label: "Anthropic",
		KeyEnv: "ANTHROPIC_API_KEY", DefaultModel: "claude-opus-5",
	},
	"openai": {
		Kind: KindOpenAICompat, Label: "OpenAI",
		KeyEnv: "OPENAI_API_KEY", DefaultModel: "gpt-5.2",
	},
	"zai": {
		Kind: KindOpenAICompat, Label: "Z.ai (pay-per-use)",
		KeyEnv: "ZAI_API_KEY", BaseURL: "https://api.z.ai/api/paas/v4/", DefaultModel: "glm-5",
	},
	"zai-coding": {
		// GLM Coding Plan subscription; its keys only work on the coding
		// endpoints (docs.z.ai/devpack) — never on the pay-per-use URL.
		Kind: KindOpenAICompat, Label: "Z.ai GLM Coding Plan",
		KeyEnv: "ZAI_API_KEY", BaseURL: "https://api.z.ai/api/coding/paas/v4/", DefaultModel: "glm-5.3",
	},
	"ollama": {
		Kind: KindOpenAICompat, Label: "Ollama (local)",
		BaseURL: "http://localhost:11434/v1",
	},
}

// Order is the stable display order used by the setup wizard.
var Order = []string{"zai-coding", "zai", "anthropic", "openai", "ollama"}

// DetectFromEnv lists provider ids whose API-key environment variable is
// set, in Order sequence. Providers without a KeyEnv are never detected.
func DetectFromEnv() []string {
	var detected []string

	for _, id := range Order {
		if preset := Presets[id]; preset.KeyEnv != "" && os.Getenv(preset.KeyEnv) != "" {
			detected = append(detected, id)
		}
	}

	return detected
}
