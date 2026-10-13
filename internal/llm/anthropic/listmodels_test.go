package anthropic_test

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/dev-hann/golem/internal/llm"
	anthropicgo "github.com/dev-hann/golem/internal/llm/anthropic"
)

func TestListModels_ParsesIDs(t *testing.T) {
	t.Parallel()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		// Wire shape follows the documented ModelList contract; endpoint
		// existence verified live (401 without a key).
		_, _ = w.Write([]byte(`{
			"data": [
				{"id": "claude-opus-5", "type": "model", "display_name": "Claude Opus 5"},
				{"id": "claude-sonnet-4", "type": "model", "display_name": "Claude Sonnet 4"}
			],
			"first_id": "claude-opus-5", "last_id": "claude-sonnet-4", "has_more": false
		}`))
	}))
	t.Cleanup(srv.Close)

	models, err := anthropicgo.ListModels(context.Background(), "test-key", srv.URL)
	if err != nil {
		t.Fatalf("ListModels: %v", err)
	}

	if len(models) != 2 || models[0] != "claude-opus-5" || models[1] != "claude-sonnet-4" {
		t.Errorf("models = %v", models)
	}
}

func TestListModels_AuthErrorIsSentinel(t *testing.T) {
	t.Parallel()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte(`{"type":"error","error":{"type":"authentication_error","message":"invalid x-api-key"}}`))
	}))
	t.Cleanup(srv.Close)

	_, err := anthropicgo.ListModels(context.Background(), "bad-key", srv.URL)
	if !errors.Is(err, llm.ErrAuth) {
		t.Errorf("expected ErrAuth, got: %v", err)
	}
}

func TestListModels_NotFoundIsUnavailable(t *testing.T) {
	t.Parallel()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNotFound)
		_, _ = w.Write([]byte(`{"type":"error","error":{"type":"not_found_error","message":"path not found"}}`))
	}))
	t.Cleanup(srv.Close)

	_, err := anthropicgo.ListModels(context.Background(), "test-key", srv.URL)
	if !errors.Is(err, llm.ErrModelListUnavailable) {
		t.Errorf("expected ErrModelListUnavailable, got: %v", err)
	}
}
