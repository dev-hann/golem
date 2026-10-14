package openai

import (
	"errors"
	"testing"

	"github.com/openai/openai-go"

	"github.com/dev-hann/golem/internal/llm"
)

func TestClassifyStreamError(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name      string
		err       error
		transient bool
		auth      bool
	}{
		{"rate limit", &openai.Error{StatusCode: 429}, true, false},
		{"server error", &openai.Error{StatusCode: 503}, true, false},
		{"unauthorized", &openai.Error{StatusCode: 401}, false, true},
		{"bad request", &openai.Error{StatusCode: 400}, false, false},
		{"network drop", errors.New("EOF"), true, false},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			got := classifyStreamError(tc.err)

			if errors.Is(got, llm.ErrTransient) != tc.transient {
				t.Errorf("transient = %v, want %v (%v)", errors.Is(got, llm.ErrTransient), tc.transient, got)
			}

			if errors.Is(got, llm.ErrAuth) != tc.auth {
				t.Errorf("auth = %v, want %v (%v)", errors.Is(got, llm.ErrAuth), tc.auth, got)
			}
		})
	}
}
