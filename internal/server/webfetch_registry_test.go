package server_test

import (
	"testing"
)

// ADR-0021: webfetch ships in the server registry so TUI turns can use it.
func TestServer_RegistryIncludesWebFetch(t *testing.T) {
	t.Parallel()

	h := newHarness(t, &scriptedProvider{})
	if _, ok := h.srv.Registry().Get("webfetch"); !ok {
		t.Error("webfetch missing from the server registry")
	}
}
