// Provider construction: maps a provider id plus resolved settings onto an
// adapter instance.

package cli

import (
	"context"
	"errors"
	"fmt"
	"os"
	"time"

	"github.com/dev-hann/golem/internal/config"
	"github.com/dev-hann/golem/internal/llm"
	anthropicgo "github.com/dev-hann/golem/internal/llm/anthropic"
	openaicompat "github.com/dev-hann/golem/internal/llm/openai"
)

// buildProvider constructs the adapter for a provider id. key is the
// resolved plaintext key ("" for keyless endpoints); baseURL overrides the
// endpoint when non-empty.
func buildProvider(id, key, baseURL string) (llm.Provider, error) {
	preset, ok := llm.Presets[id]
	if !ok {
		return nil, fmt.Errorf("cli: unknown provider %q", id)
	}

	if baseURL == "" {
		baseURL = preset.BaseURL
	}

	switch preset.Kind {
	case llm.KindAnthropic:
		return anthropicgo.NewProvider(key, baseURL), nil
	case llm.KindOpenAICompat:
		return openaicompat.NewProvider(key, baseURL), nil
	default:
		return nil, fmt.Errorf("cli: provider %q has unsupported kind %q", id, preset.Kind)
	}
}

// ProviderChoice is the outcome of provider resolution.
type ProviderChoice struct {
	ID       string
	Provider llm.Provider
	Model    string
}

// ResolveProvider loads the config at configPath and resolves the effective
// provider, model, and key (docs/CONFIG.md precedence). Without a config
// file, exactly one detected provider key selects that provider; GOLEM_
// MODEL overrides any configured or preset model.
//
// Key-resolution failures return a partially filled choice (ID and Model
// set, Provider nil) alongside the error; structural failures (parse,
// unknown provider, missing model) return an empty choice.
func ResolveProvider(configPath string) (ProviderChoice, error) {
	cfg, err := config.Load(configPath)
	if err != nil {
		return ProviderChoice{}, fmt.Errorf("cli: %w", err)
	}

	id := resolveProviderID(cfg)

	preset, ok := llm.Presets[id]
	if !ok {
		return ProviderChoice{}, fmt.Errorf("cli: unknown provider %q", id)
	}

	settings := cfg.Providers[id]

	model := firstOf(os.Getenv("GOLEM_MODEL"), settings.Model, preset.DefaultModel)
	if model == "" {
		return ProviderChoice{}, fmt.Errorf(
			"cli: provider %s requires a model — set [provider.%s] model or GOLEM_MODEL", id, id,
		)
	}

	choice := ProviderChoice{ID: id, Model: model}

	key := ""

	switch {
	case settings.APIKey != "":
		key, err = config.ResolveKey(settings.APIKey)
		if err != nil {
			return choice, fmt.Errorf("cli: %w", err)
		}
	case preset.KeyEnv != "":
		key = os.Getenv(preset.KeyEnv)
	}

	provider, err := buildProvider(id, key, settings.BaseURL)
	if err != nil {
		return ProviderChoice{}, err
	}

	choice.Provider = provider

	return choice, nil
}

// ResolveProviderForServe is the serve wiring: key problems never stop the
// boot — the choice wraps a status-aware provider that fails turns with the
// verdict instead, and the registry records the reason. Structural failures
// still return an error.
func ResolveProviderForServe(configPath string, reg *StatusRegistry) (ProviderChoice, error) {
	choice, err := ResolveProvider(configPath)
	if err == nil {
		return ProviderChoice{
			ID:       choice.ID,
			Model:    choice.Model,
			Provider: statusProvider{reg: reg, id: choice.ID, inner: choice.Provider},
		}, nil
	}

	if choice.ID != "" && errors.Is(err, config.ErrKeyUnresolved) {
		reg.Set(choice.ID, StateRejected, err.Error())

		return ProviderChoice{
			ID:       choice.ID,
			Model:    choice.Model,
			Provider: statusProvider{reg: reg, id: choice.ID},
		}, nil
	}

	return ProviderChoice{}, err
}

// resolveProviderID picks the provider id: GOLEM_PROVIDER (already folded
// into cfg.Default) > config file default > single detected env key >
// the built-in default.
func resolveProviderID(cfg *config.Config) string {
	if cfg.Path != "" || cfg.Default != config.DefaultProviderID {
		return cfg.Default
	}

	if detected := llm.DetectFromEnv(); len(detected) == 1 {
		return detected[0]
	}

	return config.DefaultProviderID
}

// firstOf returns the first non-empty string.
func firstOf(values ...string) string {
	for _, v := range values {
		if v != "" {
			return v
		}
	}

	return ""
}

// modelListTimeout bounds the wizard's live model-list fetch.
const modelListTimeout = 20 * time.Second

// LiveListModels fetches a provider's model ids from its real endpoint.
func LiveListModels(id, key string) ([]string, error) {
	preset, ok := llm.Presets[id]
	if !ok {
		return nil, fmt.Errorf("cli: unknown provider %q", id)
	}

	ctx, cancel := context.WithTimeout(context.Background(), modelListTimeout)
	defer cancel()

	switch preset.Kind {
	case llm.KindAnthropic:
		return anthropicgo.ListModels(ctx, key, preset.BaseURL)
	case llm.KindOpenAICompat:
		return openaicompat.ListModels(ctx, key, preset.BaseURL)
	default:
		return nil, fmt.Errorf("cli: provider %q has unsupported kind %q", id, preset.Kind)
	}
}
