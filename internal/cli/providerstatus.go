// Provider status: boot-time health checks for every registered provider,
// held in a registry the serve wiring consults per turn. Credential
// problems degrade turns, never the boot (layering: the server owns
// sessions and routing; keys matter only when a turn talks to a provider).

package cli

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"sync"
	"time"

	"github.com/dev-hann/golem/internal/config"
	"github.com/dev-hann/golem/internal/llm"
)

// providerProbeTimeout bounds each background provider check.
const providerProbeTimeout = 10 * time.Second

// ProviderState is a provider's boot-check verdict.
type ProviderState string

// Provider states.
const (
	StatePending     ProviderState = "pending"
	StateReady       ProviderState = "ready"
	StateNoKey       ProviderState = "no key"
	StateRejected    ProviderState = "key rejected"
	StateUnreachable ProviderState = "unreachable"
)

// ProviderStatus is one provider's health verdict.
type ProviderStatus struct {
	ID     string
	State  ProviderState
	Detail string
}

// StatusRegistry holds per-provider verdicts, safe for concurrent use.
type StatusRegistry struct {
	mu       sync.RWMutex
	statuses map[string]ProviderStatus
}

// NewStatusRegistry marks every registered provider pending.
func NewStatusRegistry() *StatusRegistry {
	reg := &StatusRegistry{statuses: map[string]ProviderStatus{}}

	for _, id := range llm.Order {
		reg.statuses[id] = ProviderStatus{ID: id, State: StatePending}
	}

	return reg
}

// Set records a verdict.
func (r *StatusRegistry) Set(id string, state ProviderState, detail string) {
	r.mu.Lock()
	defer r.mu.Unlock()

	r.statuses[id] = ProviderStatus{ID: id, State: state, Detail: detail}
}

// Get returns a provider's verdict (pending when never checked).
func (r *StatusRegistry) Get(id string) ProviderStatus {
	r.mu.RLock()
	defer r.mu.RUnlock()

	if status, ok := r.statuses[id]; ok {
		return status
	}

	return ProviderStatus{ID: id, State: StatePending}
}

// Snapshot copies all verdicts.
func (r *StatusRegistry) Snapshot() []ProviderStatus {
	r.mu.RLock()
	defer r.mu.RUnlock()

	out := make([]ProviderStatus, 0, len(r.statuses))
	for _, status := range r.statuses {
		out = append(out, status)
	}

	return out
}

// ProbeConfig parameterizes a boot-time probe sweep.
type ProbeConfig struct {
	ConfigPath string
	// ListModels is injected (tests stub it; production hits live endpoints).
	ListModels func(providerID, key string) ([]string, error)
	Log        io.Writer
}

// ProbeAll spawns one background check per registered provider, logging a
// line as each settles. It returns immediately — the boot never waits.
func (r *StatusRegistry) ProbeAll(ctx context.Context, pc ProbeConfig) {
	cfg, err := config.Load(pc.ConfigPath)
	if err != nil {
		fmt.Fprintf(pc.Log, "provider probe: config unreadable: %v\n", err) //nolint:errcheck // terminal output

		return
	}

	for _, id := range llm.Order {
		go r.probeOne(ctx, id, cfg, pc)
	}
}

// probeOne checks a single provider: resolve the key, call the model-list
// endpoint, classify.
func (r *StatusRegistry) probeOne(ctx context.Context, id string, cfg *config.Config, pc ProbeConfig) {
	preset, ok := llm.Presets[id]
	if !ok {
		r.Set(id, StateNoKey, "custom provider, not probed")

		return
	}

	key, state, detail := resolveProbeKey(cfg, id, preset)
	if state != StateReady {
		r.Set(id, state, detail)
		r.log(pc, id, state, detail)

		return
	}

	checkCtx, cancel := context.WithTimeout(ctx, providerProbeTimeout)
	defer cancel()

	models, err := probeWith(checkCtx, pc, id, key)
	switch {
	case err == nil:
		detail = fmt.Sprintf("%d models", len(models))
	case errors.Is(err, llm.ErrAuth):
		r.Set(id, StateRejected, err.Error())
		r.log(pc, id, StateRejected, err.Error())

		return
	case errors.Is(err, llm.ErrModelListUnavailable):
		// Auth passed (a 404, not a 401): the key is verified even though
		// the endpoint offers no listing.
		detail = "key verified (no model list)"
	default:
		r.Set(id, StateUnreachable, err.Error())
		r.log(pc, id, StateUnreachable, err.Error())

		return
	}

	r.Set(id, StateReady, detail)
	r.log(pc, id, StateReady, detail)
}

// probeWith runs the injected check with a deadline even when the stub
// ignores ctx.
func probeWith(ctx context.Context, pc ProbeConfig, id, key string) ([]string, error) {
	type result struct {
		models []string
		err    error
	}

	done := make(chan result, 1)

	go func() {
		models, err := pc.ListModels(id, key)
		done <- result{models, err}
	}()

	select {
	case res := <-done:
		return res.models, res.err
	case <-ctx.Done():
		return nil, fmt.Errorf("probe timeout: %w", ctx.Err())
	}
}

// resolveProbeKey resolves a provider's key for probing; the returned state
// is StateReady when a check makes sense, or the reason it does not.
func resolveProbeKey(
	cfg *config.Config,
	id string,
	preset llm.Preset,
) (key string, state ProviderState, detail string) {
	settings := cfg.Providers[id]

	key = settings.APIKey
	if key != "" {
		resolved, err := config.ResolveKey(key)
		if err != nil {
			return "", StateRejected, err.Error()
		}

		key = resolved
	} else if preset.KeyEnv != "" {
		key = os.Getenv(preset.KeyEnv)
	}

	if preset.KeyEnv != "" && key == "" {
		return "", StateNoKey, "no key configured (golem setup)"
	}

	return key, StateReady, ""
}

// log prints one probe result line.
func (r *StatusRegistry) log(pc ProbeConfig, id string, state ProviderState, detail string) {
	fmt.Fprintf(pc.Log, "provider %s: %s — %s\n", id, state, detail) //nolint:errcheck // terminal output
}

// WrapWithStatus wraps a provider with registry awareness: blocked states
// fail turns with the verdict, pending and ready states delegate.
func WrapWithStatus(reg *StatusRegistry, id string, inner llm.Provider) llm.Provider {
	return statusProvider{reg: reg, id: id, inner: inner}
}

// statusProvider wraps a provider with registry awareness: a provider the
// boot check rejected fails turns immediately with the verdict instead of
// re-deriving the failure; pending or ready providers stream normally.
type statusProvider struct {
	reg   *StatusRegistry
	id    string
	inner llm.Provider
}

// Stream implements llm.Provider.
func (p statusProvider) Stream(ctx context.Context, req llm.Request) (<-chan llm.Event, error) {
	if status := p.reg.Get(p.id); status.State == StateRejected ||
		status.State == StateUnreachable ||
		status.State == StateNoKey {
		out := make(chan llm.Event, 1)
		out <- llm.Event{
			Type: llm.EventError,
			Err:  fmt.Errorf("provider %s: %s (%s) — fix: golem setup", p.id, status.State, status.Detail),
		}

		close(out)

		return out, nil
	}

	if p.inner == nil {
		out := make(chan llm.Event, 1)
		out <- llm.Event{
			Type: llm.EventError,
			Err:  fmt.Errorf("provider %s: no usable provider wired", p.id),
		}

		close(out)

		return out, nil
	}

	return p.inner.Stream(ctx, req)
}
