// Provider construction: maps a provider id plus resolved settings onto an
// adapter instance.

package cli

import (
	"context"
	"fmt"
	"io"
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
func ResolveProvider(configPath string, warn io.Writer) (ProviderChoice, error) {
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
			"cli: provider %s requires a model — set [provider.%s] model or GOLEM_MODEL", id, id)
	}

	key := ""

	switch {
	case settings.APIKey != "":
		key, err = config.ResolveKey(settings.APIKey, warn)
		if err != nil {
			return ProviderChoice{}, fmt.Errorf("cli: %w", err)
		}
	case preset.KeyEnv != "":
		key = os.Getenv(preset.KeyEnv)
	}

	provider, err := buildProvider(id, key, settings.BaseURL)
	if err != nil {
		return ProviderChoice{}, err
	}

	return ProviderChoice{ID: id, Provider: provider, Model: model}, nil
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

// keyTestTimeout bounds the wizard's live key probe.
const keyTestTimeout = 20 * time.Second

// liveKeyTest proves a key with a one-word round trip against the provider.
func liveKeyTest(id, key, model string) error {
	provider, err := buildProvider(id, key, "")
	if err != nil {
		return err
	}

	ctx, cancel := context.WithTimeout(context.Background(), keyTestTimeout)
	defer cancel()

	stream, err := provider.Stream(ctx, llm.Request{
		Model: model, Messages: []llm.Message{{Role: llm.RoleUser, Text: "ping"}},
	})
	if err != nil {
		return fmt.Errorf("key test: %w", err)
	}

	for event := range stream {
		if event.Type == llm.EventError {
			return fmt.Errorf("key test: %w", event.Err)
		}
	}

	return nil
}
