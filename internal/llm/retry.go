// Retry wrapper for providers: bounded, backoff-driven retries of
// transient failures that happen before any content has streamed.

package llm

import (
	"context"
	"errors"
	"fmt"
	"os"
	"time"
)

// ErrTransient marks provider failures worth retrying (rate limits,
// overload, network drops). Adapters classify their SDK errors onto it.
var ErrTransient = errors.New("transient provider failure")

// MarkTransient tags err as retryable while preserving its chain; nil
// passes through unchanged.
func MarkTransient(err error) error {
	if err == nil {
		return nil
	}

	return fmt.Errorf("%w: %w", err, ErrTransient)
}

// MarkAuth tags err as a credential rejection while preserving its chain.
func MarkAuth(err error) error {
	if err == nil {
		return nil
	}

	return fmt.Errorf("%w: %w", err, ErrAuth)
}

// Retry defaults: six retries after the initial attempt, 1s doubling to a
// 30s cap (~61s of waiting at the limit).
const (
	defaultRetries    = 6
	defaultBackoff    = 1 * time.Second
	defaultMaxBackoff = 30 * time.Second
)

// RetryPolicy bounds the retry wrapper.
type RetryPolicy struct {
	// Retries is the retry count after the initial attempt.
	Retries int
	// Backoff is the first retry delay; it doubles per retry.
	Backoff time.Duration
	// MaxBackoff caps a single retry delay.
	MaxBackoff time.Duration
	// Sleep waits one backoff; returning false means ctx ended. Injectable
	// for tests.
	Sleep func(ctx context.Context, d time.Duration) bool
}

// DefaultRetryPolicy returns the standard retry configuration.
func DefaultRetryPolicy() RetryPolicy {
	return RetryPolicy{
		Retries:    defaultRetries,
		Backoff:    defaultBackoff,
		MaxBackoff: defaultMaxBackoff,
		Sleep:      sleepCtx,
	}
}

// sleepCtx waits for d unless ctx ends first.
func sleepCtx(ctx context.Context, d time.Duration) bool {
	timer := time.NewTimer(d)
	defer timer.Stop()

	select {
	case <-ctx.Done():
		return false
	case <-timer.C:
		return true
	}
}

// retryStreamBuffer sizes the wrapper's event channel (matches adapters).
const retryStreamBuffer = 64

// retryingProvider wraps a Provider with the retry policy.
type retryingProvider struct {
	inner  Provider
	policy RetryPolicy
}

// Retrying wraps p so transient failures are retried with a doubling
// backoff — but only before any content (text delta or tool call) has
// streamed: the transcript already holds partial output after that point,
// and a retry would duplicate it. Auth failures and unmarked errors pass
// through untouched.
func Retrying(p Provider, policy RetryPolicy) Provider {
	return &retryingProvider{inner: p, policy: policy}
}

// Stream implements Provider; the retry loop runs in its own goroutine so
// callers always get an event channel immediately.
func (r *retryingProvider) Stream(ctx context.Context, req Request) (<-chan Event, error) {
	out := make(chan Event, retryStreamBuffer)

	go r.loop(ctx, req, out)

	return out, nil
}

// loop drives attempts until the stream completes, a non-retryable
// failure surfaces, or the retries run out.
func (r *retryingProvider) loop(ctx context.Context, req Request, out chan<- Event) {
	defer close(out)

	send := makeRetrySend(ctx, out)

	for attempt := 0; ; attempt++ {
		if attempt > 0 {
			delay := min(r.policy.Backoff<<(attempt-1), r.policy.MaxBackoff)

			// Announce the wait so clients can render a retrying state
			// instead of a frozen one (the pre-content window can reach
			// ~61s; a silent stall reads as a hang).
			if !send(Event{
				Type: EventRetry, Attempt: attempt, Retries: r.policy.Retries,
				DelayMs: int(delay / time.Millisecond),
			}) {
				return
			}

			if !r.policy.Sleep(ctx, delay) {
				send(Event{Type: EventError, Err: fmt.Errorf("retry wait: %w", ctx.Err())})

				return
			}

			fmt.Fprintf(os.Stderr, "golem: provider retry %d/%d in %s\n", //nolint:errcheck // log line
				attempt, r.policy.Retries, delay)
		}

		events, err := r.inner.Stream(ctx, req)
		if err != nil {
			if attempt >= r.policy.Retries || !retryable(err) {
				send(Event{Type: EventError, Err: err})

				return
			}

			continue
		}

		if r.pump(events, send, attempt) {
			return // completed or surfaced to the consumer
		}
		// else: transient pre-content failure — loop into the next retry.
	}
}

// pump forwards one attempt's events. It reports true when the stream
// finished (successfully or with a surfaced error) and false when a
// transient pre-content failure deserves a retry.
func (r *retryingProvider) pump(
	events <-chan Event, send func(Event) bool, attempt int,
) bool {
	content := false

	for ev := range events {
		if ev.Type == EventError {
			if content || attempt >= r.policy.Retries || !retryable(ev.Err) {
				send(ev)

				return true
			}

			return false
		}

		if ev.Type == EventTextDelta || ev.Type == EventToolCalls {
			content = true
		}

		if !send(ev) {
			return true // consumer gone
		}
	}

	return true
}

// retryable classifies one failure.
func retryable(err error) bool {
	if err == nil {
		return false
	}

	if errors.Is(err, ErrAuth) || errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return false
	}

	return errors.Is(err, ErrTransient)
}

// makeRetrySend prefers delivering events and only gives up when the
// consumer is gone and the buffer is full while ctx is cancelled.
func makeRetrySend(ctx context.Context, out chan<- Event) func(Event) bool {
	return func(ev Event) bool {
		select {
		case out <- ev:
			return true
		default:
		}

		select {
		case out <- ev:
			return true
		case <-ctx.Done():
			return false
		}
	}
}
