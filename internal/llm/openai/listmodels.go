// Model listing: proves credentials and feeds the setup wizard's picker.

package openai

import (
	"context"
	"errors"
	"fmt"

	"github.com/openai/openai-go"
	"github.com/openai/openai-go/option"

	"github.com/dev-hann/golem/internal/llm"
)

// ListModels returns the endpoint's model ids (works for openai, zai,
// ollama, and gateways — any OpenAI-compatible /v1/models). Auth failures
// wrap llm.ErrAuth; a missing or empty listing wraps
// llm.ErrModelListUnavailable (the key may still be valid).
func ListModels(ctx context.Context, apiKey, baseURL string) ([]string, error) {
	opts := []option.RequestOption{}
	if apiKey != "" {
		opts = append(opts, option.WithAPIKey(apiKey))
	}

	if baseURL != "" {
		opts = append(opts, option.WithBaseURL(baseURL))
	}

	client := openai.NewClient(opts...)

	page, err := client.Models.List(ctx)
	if err != nil {
		return nil, classifyListError(err)
	}

	ids := make([]string, 0, len(page.Data))
	for _, model := range page.Data {
		ids = append(ids, model.ID)
	}

	if len(ids) == 0 {
		return nil, fmt.Errorf("%w: endpoint returned no models", llm.ErrModelListUnavailable)
	}

	return ids, nil
}

// classifyListError maps SDK/API errors onto the llm sentinels.
func classifyListError(err error) error {
	var apiErr *openai.Error
	if errors.As(err, &apiErr) && apiErr.StatusCode >= 400 && apiErr.StatusCode < 500 {
		if apiErr.StatusCode == httpUnauthorized || apiErr.StatusCode == httpForbidden {
			return fmt.Errorf("%w: %w", llm.ErrAuth, err)
		}

		return fmt.Errorf("%w: %w", llm.ErrModelListUnavailable, err)
	}

	return fmt.Errorf("models: %w", err)
}

// HTTP statuses used for classification, named per lint rules.
const (
	httpUnauthorized = 401
	httpForbidden    = 403
)
