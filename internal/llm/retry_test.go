package llm_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/dev-hann/golem/internal/llm"
)

// retryProviderStub scripts Stream rounds: each round yields its events
// then closes. A round of nil means "return a sync error with err".
type retryProviderStub struct {
	rounds [][]llm.Event
	errs   []error
	next   int
}

func (p *retryProviderStub) Stream(
	_ context.Context,
	_ llm.Request,
) (<-chan llm.Event, error) {
	i := p.next
	p.next++

	if i < len(p.errs) && p.errs[i] != nil {
		return nil, p.errs[i]
	}

	if i >= len(p.rounds) {
		return nil, errors.New("stub: no more rounds")
	}

	out := make(chan llm.Event, len(p.rounds[i])+1)

	for _, ev := range p.rounds[i] {
		out <- ev
	}

	close(out)

	return out, nil
}

// recorderSleep collects retry delays instead of waiting.
type recorderSleep struct {
	delays []time.Duration
}

func (r *recorderSleep) sleep(_ context.Context, d time.Duration) bool {
	r.delays = append(r.delays, d)
	return true
}

func readAll(t *testing.T, events <-chan llm.Event) []llm.Event {
	t.Helper()

	var out []llm.Event

	for ev := range events {
		out = append(out, ev)
	}

	return out
}

// streamAll drives one wrapped Stream call to completion.
func streamAll(t *testing.T, p llm.Provider) []llm.Event {
	t.Helper()

	events, err := p.Stream(t.Context(), llm.Request{Model: "m"})
	if err != nil {
		t.Fatalf("Stream: %v", err)
	}

	return readAll(t, events)
}

func hasError(events []llm.Event) bool {
	for _, ev := range events {
		if ev.Type == llm.EventError {
			return true
		}
	}

	return false
}

func policyForTesting(sleep *recorderSleep) llm.RetryPolicy {
	p := llm.DefaultRetryPolicy()
	p.Sleep = sleep.sleep

	return p
}

func TestRetrying_PreContentFailuresAreRetried(t *testing.T) {
	t.Parallel()

	stub := &retryProviderStub{
		errs: []error{llm.MarkTransient(errors.New("boom"))},
		rounds: [][]llm.Event{
			{}, // round 0: sync error path (errs[0] wins)
			{ // round 1: stream opens, dies before any content
				{Type: llm.EventError, Err: llm.MarkTransient(errors.New("429"))},
			},
			{ // round 2: success
				{Type: llm.EventTextDelta, Delta: "hello"},
				{Type: llm.EventUsage},
			},
		},
	}
	sleep := &recorderSleep{}

	events := streamAll(t, llm.Retrying(stub, policyForTesting(sleep)))

	if hasError(events) {
		t.Fatalf("transient pre-content failures must be retried: %+v", events)
	}

	// Each retry announces itself before sleeping: 2 failures → 2 retry
	// events, then the successful round's stream.
	if len(events) != 4 {
		t.Fatalf("events = %+v, want 2 retry announcements + text + usage", events)
	}

	for i, want := range []int{1, 2} {
		ev := events[i]
		if ev.Type != llm.EventRetry || ev.Attempt != want || ev.Retries != 6 ||
			ev.DelayMs != want*1000 {
			t.Errorf("events[%d] = %+v, want retry attempt %d (cap 6, %dms)", i, ev, want, want*1000)
		}
	}

	if events[2].Delta != "hello" {
		t.Errorf("events = %+v, want the successful round's stream after the retries", events)
	}

	want := []time.Duration{time.Second, 2 * time.Second}
	if len(sleep.delays) != len(want) {
		t.Fatalf("delays = %v, want %v", sleep.delays, want)
	}

	for i := range want {
		if sleep.delays[i] != want[i] {
			t.Errorf("delay[%d] = %v, want %v (doubling backoff)", i, sleep.delays[i], want[i])
		}
	}
}

func TestRetrying_FailureAfterContentIsNotRetried(t *testing.T) {
	t.Parallel()

	stub := &retryProviderStub{
		rounds: [][]llm.Event{
			{
				{Type: llm.EventTextDelta, Delta: "partial"},
				{Type: llm.EventError, Err: llm.MarkTransient(errors.New("mid-stream drop"))},
			},
			{{Type: llm.EventTextDelta, Delta: "must not run"}},
		},
	}
	sleep := &recorderSleep{}

	events := streamAll(t, llm.Retrying(stub, policyForTesting(sleep)))

	if !hasError(events) {
		t.Fatal("mid-stream failure must surface to the consumer")
	}

	if stub.next != 1 {
		t.Errorf("attempts = %d, want 1 — content already streamed, retrying would duplicate", stub.next)
	}

	if len(sleep.delays) != 0 {
		t.Errorf("delays = %v, want none", sleep.delays)
	}
}

func TestRetrying_AuthFailsImmediately(t *testing.T) {
	t.Parallel()

	stub := &retryProviderStub{
		errs: []error{llm.ErrAuth},
		rounds: [][]llm.Event{
			{{Type: llm.EventTextDelta, Delta: "unreached"}},
		},
	}
	sleep := &recorderSleep{}

	events := streamAll(t, llm.Retrying(stub, policyForTesting(sleep)))

	if !hasError(events) {
		t.Fatal("auth failure must surface as an error event")
	}

	if stub.next != 1 || len(sleep.delays) != 0 {
		t.Errorf("auth must not be retried: attempts=%d delays=%v", stub.next, sleep.delays)
	}
}

func TestRetrying_ExhaustedForwardsLastError(t *testing.T) {
	t.Parallel()

	last := llm.MarkTransient(errors.New("still limited"))
	stub := &retryProviderStub{
		errs: []error{last, last, last, last, last, last, last, last},
	}
	sleep := &recorderSleep{}

	events := streamAll(t, llm.Retrying(stub, policyForTesting(sleep)))

	if !hasError(events) {
		t.Fatal("exhausted retries must surface the final error")
	}

	// Initial attempt + 6 retries.
	if stub.next != 7 {
		t.Errorf("attempts = %d, want 7 (initial + 6 retries)", stub.next)
	}
}

func TestRetrying_BackoffDoublesAndCaps(t *testing.T) {
	t.Parallel()

	limit := llm.MarkTransient(errors.New("no"))
	stub := &retryProviderStub{errs: []error{limit, limit, limit, limit, limit, limit, limit, limit}}
	sleep := &recorderSleep{}

	streamAll(t, llm.Retrying(stub, policyForTesting(sleep)))

	want := []time.Duration{
		1 * time.Second,
		2 * time.Second,
		4 * time.Second,
		8 * time.Second,
		16 * time.Second,
		30 * time.Second,
	}

	if len(sleep.delays) != len(want) {
		t.Fatalf("delays = %v, want %v", sleep.delays, want)
	}

	for i := range want {
		if sleep.delays[i] != want[i] {
			t.Errorf("delay[%d] = %v, want %v", i, sleep.delays[i], want[i])
		}
	}
}

func TestRetrying_UnmarkedErrorsAreNotRetried(t *testing.T) {
	t.Parallel()

	plain := errors.New("bad request")
	stub := &retryProviderStub{errs: []error{plain}}
	sleep := &recorderSleep{}

	events := streamAll(t, llm.Retrying(stub, policyForTesting(sleep)))

	if !hasError(events) {
		t.Fatal("unclassified sync error must surface (Stream returned error)")
	}

	if len(sleep.delays) != 0 {
		t.Errorf("unmarked errors must not be retried: %v", sleep.delays)
	}
}
