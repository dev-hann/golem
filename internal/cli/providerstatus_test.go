package cli_test

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/dev-hann/golem/internal/cli"
	"github.com/dev-hann/golem/internal/llm"
)

// lockedWriter serializes writes across probe goroutines.
type lockedWriter struct {
	mu  sync.Mutex
	buf strings.Builder
}

func (w *lockedWriter) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()

	return w.buf.Write(p)
}

func (w *lockedWriter) String() string {
	w.mu.Lock()
	defer w.mu.Unlock()

	return w.buf.String()
}

// waitSettled polls until every provider id reaches a non-pending state.
func waitSettled(t *testing.T, reg *cli.StatusRegistry, ids ...string) bool {
	t.Helper()

	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		settled := true

		for _, id := range ids {
			if reg.Get(id).State == cli.StatePending {
				settled = false
			}
		}

		if settled {
			return true
		}

		time.Sleep(5 * time.Millisecond)
	}

	return false
}

func writeProbeConfig(t *testing.T, body string) string {
	t.Helper()

	path := filepath.Join(t.TempDir(), "config.toml")
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}

	return path
}

func TestProbeAll_ClassifiesEveryRegisteredProvider(t *testing.T) {
	clearProviderKeys(t)

	cfgPath := writeProbeConfig(t, `
[provider]
default = "zai"

[provider.zai]
api_key = "sk-zai"

[provider.openai]
api_key = "sk-openai"
`)

	reg := cli.NewStatusRegistry()
	log := &lockedWriter{}

	reg.ProbeAll(context.Background(), cli.ProbeConfig{
		ConfigPath: cfgPath,
		ListModels: func(id, _ string) ([]string, error) {
			switch id {
			case "zai":
				return []string{"glm-5", "glm-5.3"}, nil
			case "openai":
				return nil, errors.New("boom")
			default:
				return nil, llm.ErrModelListUnavailable
			}
		},
		Log: log,
	})

	if !waitSettled(t, reg, "zai", "anthropic", "openai", "ollama") {
		t.Fatal("probes did not settle within 3s")
	}

	if st := reg.Get("zai"); st.State != cli.StateReady || !strings.Contains(st.Detail, "2 models") {
		t.Errorf("zai = %+v, want ready/2 models", st)
	}
	if st := reg.Get("anthropic"); st.State != cli.StateNoKey {
		t.Errorf("anthropic = %+v, want no key (skipped network)", st)
	}
	if st := reg.Get("openai"); st.State != cli.StateUnreachable {
		t.Errorf("openai = %+v, want unreachable on network error", st)
	}
	// ollama is keyless: its stub answer is ErrModelListUnavailable, which
	// counts as "key verified" — but with no key the probe checks the local
	// endpoint; the stub returns unavailable → ready-with-caveat.
	if st := reg.Get("ollama"); st.State != cli.StateReady {
		t.Errorf("ollama = %+v, want ready (list unavailable counts as verified)", st)
	}
}

func TestProbeAll_RejectedKeyIsState(t *testing.T) {
	clearProviderKeys(t)

	cfgPath := writeProbeConfig(t, `
[provider]
default = "zai"

[provider.zai]
api_key = "sk-bad"
`)

	reg := cli.NewStatusRegistry()

	reg.ProbeAll(context.Background(), cli.ProbeConfig{
		ConfigPath: cfgPath,
		ListModels: func(_, _ string) ([]string, error) {
			return nil, llm.ErrAuth
		},
		Log: os.Stderr,
	})

	if !waitSettled(t, reg, "zai") {
		t.Fatal("probe did not settle")
	}

	if st := reg.Get("zai"); st.State != cli.StateRejected {
		t.Errorf("zai = %+v, want rejected", st)
	}
}

func TestProbeAll_BrokenRefIsRejected(t *testing.T) {
	clearProviderKeys(t)

	cfgPath := writeProbeConfig(t, `
[provider]
default = "zai"

[provider.zai]
api_key = "ref:GOLEM_MISSING_VAR"
`)

	reg := cli.NewStatusRegistry()

	reg.ProbeAll(context.Background(), cli.ProbeConfig{
		ConfigPath: cfgPath,
		ListModels: func(_, _ string) ([]string, error) { return nil, nil },
		Log:        os.Stderr,
	})

	if !waitSettled(t, reg, "zai") {
		t.Fatal("probe did not settle")
	}

	if st := reg.Get("zai"); st.State != cli.StateRejected || !strings.Contains(st.Detail, "GOLEM_MISSING_VAR") {
		t.Errorf("zai = %+v, want rejected naming the variable", st)
	}
}

// recordingProvider counts inner Stream calls.
type recordingProvider struct{ calls int }

func (p *recordingProvider) Stream(
	_ context.Context,
	_ llm.Request,
) (<-chan llm.Event, error) {
	p.calls++

	out := make(chan llm.Event, 1)
	close(out)

	return out, nil
}

func TestStatusProvider_BlockedStateFailsTurnWithoutInner(t *testing.T) {
	t.Parallel()

	reg := cli.NewStatusRegistry()
	reg.Set("zai", cli.StateRejected, "401 from boot check")

	inner := &recordingProvider{}
	provider := cli.WrapWithStatus(reg, "zai", inner)

	events, err := provider.Stream(context.Background(), llm.Request{Model: "glm-5"})
	if err != nil {
		t.Fatalf("Stream: %v", err)
	}

	var got []llm.Event
	for ev := range events {
		got = append(got, ev)
	}

	if len(got) != 1 || got[0].Type != llm.EventError || !strings.Contains(got[0].Err.Error(), "golem setup") {
		t.Fatalf("events = %+v, want actionable error", got)
	}
	if inner.calls != 0 {
		t.Errorf("inner called %d times, want 0", inner.calls)
	}
}

func TestStatusProvider_ReadyDelegates(t *testing.T) {
	t.Parallel()

	reg := cli.NewStatusRegistry()
	reg.Set("zai", cli.StateReady, "2 models")

	inner := &recordingProvider{}
	provider := cli.WrapWithStatus(reg, "zai", inner)

	if _, err := provider.Stream(context.Background(), llm.Request{Model: "glm-5"}); err != nil {
		t.Fatalf("Stream: %v", err)
	}
	if inner.calls != 1 {
		t.Errorf("inner called %d times, want 1", inner.calls)
	}
}

func TestResolveProviderForServe_BrokenRefStillBoots(t *testing.T) {
	clearProviderKeys(t)

	cfgPath := writeProbeConfig(t, `
[provider]
default = "zai"

[provider.zai]
model = "glm-5"
api_key = "ref:GOLEM_MISSING_VAR"
`)

	reg := cli.NewStatusRegistry()

	choice, err := cli.ResolveProviderForServe(cfgPath, reg)
	if err != nil {
		t.Fatalf("serve resolution must not fail on key problems: %v", err)
	}

	if choice.ID != "zai" || choice.Model != "glm-5" || choice.Provider == nil {
		t.Errorf("choice = %+v, want zai/glm-5 with a provider", choice)
	}

	if st := reg.Get("zai"); st.State != cli.StateRejected {
		t.Errorf("registry = %+v, want rejected", st)
	}
}

func TestResolveProviderForServe_StructuralFailureIsFatal(t *testing.T) {
	t.Parallel()

	cfgPath := writeProbeConfig(t, "[provider\nbroken")

	if _, err := cli.ResolveProviderForServe(cfgPath, cli.NewStatusRegistry()); err == nil {
		t.Error("parse errors must stay fatal")
	}
}
