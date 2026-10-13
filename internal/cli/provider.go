// Provider construction: maps a provider id plus resolved settings onto an
// adapter instance.

package cli

import (
	"context"
	"fmt"
	"time"

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
