// Model listing: proves credentials and feeds the setup wizard's picker.

package anthropic

import (
	"context"
	"errors"
	"fmt"

	"github.com/anthropics/anthropic-sdk-go"
	"github.com/anthropics/anthropic-sdk-go/option"

	"github.com/dev-hann/golem/internal/llm"
)

// ListModels returns the account's model ids. Auth failures wrap
// llm.ErrAuth (retry the key); a missing or empty listing wraps
// llm.ErrModelListUnavailable (key may still be valid).
func ListModels(ctx context.Context, apiKey, baseURL string) ([]string, error) {
	opts := []option.RequestOption{}
	if apiKey != "" {
		opts = append(opts, option.WithAPIKey(apiKey))
	}

	if baseURL != "" {
		opts = append(opts, option.WithBaseURL(baseURL))
	}

	client := anthropic.NewClient(opts...)

	page, err := client.Models.List(ctx, anthropic.ModelListParams{})
	if err != nil {
		return nil, classifyListError(err)
	}

	ids := make([]string, 0, len(page.Data))
	for _, model := range page.Data {
		ids = append(ids, model.ID)
	}

	if len(ids) == 0 {
		return nil, fmt.Errorf("%w: anthropic returned no models", llm.ErrModelListUnavailable)
	}

	return ids, nil
}

// classifyListError maps SDK/API errors onto the llm sentinels.
func classifyListError(err error) error {
	var apiErr *anthropic.Error
	if errors.As(err, &apiErr) && apiErr.StatusCode >= 400 && apiErr.StatusCode < 500 {
		if apiErr.StatusCode == httpUnauthorized || apiErr.StatusCode == httpForbidden {
			return fmt.Errorf("%w: %w", llm.ErrAuth, err)
		}

		return fmt.Errorf("%w: %w", llm.ErrModelListUnavailable, err)
	}

	return fmt.Errorf("anthropic models: %w", err)
}

// HTTP statuses used for classification, named per lint rules.
const (
	httpUnauthorized = 401
	httpForbidden    = 403
)
