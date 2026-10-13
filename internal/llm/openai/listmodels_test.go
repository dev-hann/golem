package openai_test

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/dev-hann/golem/internal/llm"
	openaicompat "github.com/dev-hann/golem/internal/llm/openai"
)

func TestListModels_ParsesIDs(t *testing.T) {
	t.Parallel()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"object":"list","data":[
			{"id":"glm-4.7","object":"model","created":0,"owned_by":"zai"},
			{"id":"glm-5","object":"model","created":0,"owned_by":"zai"}
		]}`))
	}))
	t.Cleanup(srv.Close)

	models, err := openaicompat.ListModels(context.Background(), "test-key", srv.URL)
	if err != nil {
		t.Fatalf("ListModels: %v", err)
	}

	if len(models) != 2 || models[0] != "glm-4.7" || models[1] != "glm-5" {
		t.Errorf("models = %v", models)
	}
}

func TestListModels_KeylessLocalEndpoint(t *testing.T) {
	t.Parallel()

	var sawAuth bool

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		sawAuth = r.Header.Get("Authorization") != ""
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"object":"list","data":[{"id":"llama3.3","object":"model","created":0,"owned_by":"ollama"}]}`))
	}))
	t.Cleanup(srv.Close)

	models, err := openaicompat.ListModels(context.Background(), "", srv.URL)
	if err != nil {
		t.Fatalf("ListModels: %v", err)
	}

	if len(models) != 1 || models[0] != "llama3.3" {
		t.Errorf("models = %v", models)
	}
	if sawAuth {
		t.Error("empty key must not send an Authorization header")
	}
}

func TestListModels_AuthErrorIsSentinel(t *testing.T) {
	t.Parallel()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte(`{"error":{"message":"Incorrect API key","type":"invalid_request_error","code":"invalid_api_key"}}`))
	}))
	t.Cleanup(srv.Close)

	_, err := openaicompat.ListModels(context.Background(), "bad-key", srv.URL)
	if !errors.Is(err, llm.ErrAuth) {
		t.Errorf("expected ErrAuth, got: %v", err)
	}
}

func TestListModels_NotFoundIsUnavailable(t *testing.T) {
	t.Parallel()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	}))
	t.Cleanup(srv.Close)

	_, err := openaicompat.ListModels(context.Background(), "test-key", srv.URL)
	if !errors.Is(err, llm.ErrModelListUnavailable) {
		t.Errorf("expected ErrModelListUnavailable, got: %v", err)
	}
}

func TestListModels_EmptyListIsUnavailable(t *testing.T) {
	t.Parallel()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"object":"list","data":[]}`))
	}))
	t.Cleanup(srv.Close)

	_, err := openaicompat.ListModels(context.Background(), "test-key", srv.URL)
	if !errors.Is(err, llm.ErrModelListUnavailable) {
		t.Errorf("expected ErrModelListUnavailable on empty list, got: %v", err)
	}
}
