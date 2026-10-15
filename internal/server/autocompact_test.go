package server_test

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/dev-hann/golem/internal/llm"
	"github.com/dev-hann/golem/internal/server"
	"github.com/dev-hann/golem/pkg/golemclient"
)

// ADR-0025: when the folded history reaches the configured byte
// threshold, the server compacts it automatically before the turn's
// agent loop runs — same barrier record as manual /compact, the user
// message survives, and a failed compaction never blocks the turn.

// compactTurnEvents is one scripted round: text then finish.
var compactTurnEvents = []llm.Event{
	{Type: llm.EventTextDelta, Delta: "answer"},
	{Type: llm.EventUsage, InputTokens: 1, OutputTokens: 1},
}

// giantText exceeds the byte threshold with one user message.
func giantText(prefix string) string {
	return prefix + strings.Repeat("x", server.DefaultAutoCompactBytes)
}

// TestAutoCompact_CompactsBeforeTurnWhenOverThreshold drives a session
// past the threshold via direct store appends, then sends a turn and
// asserts the barrier lands before turn_start, the summary replaces the
// giant history, and the turn's own messages fold after the barrier.
func TestAutoCompact_CompactsBeforeTurnWhenOverThreshold(t *testing.T) {
	t.Parallel()

	provider := &compactProvider{
		scriptedProvider: scriptedProvider{rounds: [][]llm.Event{compactTurnEvents}},
		summaryEvents:    []llm.Event{{Type: llm.EventTextDelta, Delta: "SUMMARY-OF-PAST"}},
	}
	h := newHarnessWithConfig(t, provider, server.Config{
		AutoCompactBytes: server.DefaultAutoCompactBytes,
	})
	id := h.createSessionTracked()

	// Seed history past the threshold by appending raw wire events: two
	// complete turns whose combined text exceeds DefaultAutoCompactBytes.
	for i := range 2 {
		appendWire(t, h, id, []string{
			`{"type":"user_message","text":"` + giantText("q") + `"}`,
		})
		_ = i
		appendWire(t, h, id, []string{
			`{"type":"text_delta","delta":"` + giantText("a") + `"}`,
			`{"type":"turn_end","usage":{"input_tokens":1,"output_tokens":1}}`,
		})
	}

	h.runTurn(t, "fresh question")

	// The turn ran; now verify the barrier persisted and folded.
	sess, err := h.store.Get(id)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}

	if len(sess.Messages) != 3 {
		t.Fatalf("messages = %d, want 3 (summary + fresh user + answer):\n%+v",
			len(sess.Messages), sess.Messages)
	}

	if !strings.Contains(sess.Messages[0].Text, "SUMMARY-OF-PAST") {
		t.Errorf("first message = %.80q…, want the compaction summary", sess.Messages[0].Text)
	}
	if sess.Messages[1].Text != "fresh question" {
		t.Errorf("second message = %q, want the post-barrier user message", sess.Messages[1].Text)
	}
	if sess.Messages[2].Text != "answer" {
		t.Errorf("third message = %q, want the post-barrier assistant reply", sess.Messages[2].Text)
	}

	if !provider.sawSummary {
		t.Error("the summary side call never ran")
	}
}

// TestAutoCompact_UnderThresholdRunsTurnUntouched asserts small
// histories never trigger the side call.
func TestAutoCompact_UnderThresholdRunsTurnUntouched(t *testing.T) {
	t.Parallel()

	provider := &compactProvider{
		scriptedProvider: scriptedProvider{rounds: [][]llm.Event{compactTurnEvents}},
	}
	h := newHarnessWithConfig(t, provider, server.Config{
		AutoCompactBytes: server.DefaultAutoCompactBytes,
	})
	id := h.createSessionTracked()

	appendWire(t, h, id, []string{
		`{"type":"user_message","text":"tiny"}`,
		`{"type":"text_delta","delta":"tiny answer"}`,
		`{"type":"turn_end","usage":{"input_tokens":1,"output_tokens":1}}`,
	})

	h.runTurn(t, "next small question")

	if provider.sawSummary {
		t.Error("summary side call ran under the threshold")
	}

	sess, err := h.store.Get(id)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}

	if len(sess.Messages) != 4 {
		t.Fatalf("messages = %d, want 4 (two intact turns), got:\n%+v", len(sess.Messages), sess.Messages)
	}
}

// TestAutoCompact_FailureEmitsCompactFailedAndContinues asserts a
// failing summary side call emits compact_failed before the turn and
// the turn still runs on the un-compacted history.
func TestAutoCompact_FailureEmitsCompactFailedAndContinues(t *testing.T) {
	t.Parallel()

	provider := &compactProvider{
		scriptedProvider: scriptedProvider{rounds: [][]llm.Event{compactTurnEvents}},
		summaryErr:       true,
	}
	h := newHarnessWithConfig(t, provider, server.Config{
		AutoCompactBytes: server.DefaultAutoCompactBytes,
	})
	id := h.createSessionTracked()

	appendWire(t, h, id, []string{
		`{"type":"user_message","text":"` + giantText("q") + `"}`,
		`{"type":"text_delta","delta":"` + giantText("a") + `"}`,
		`{"type":"turn_end","usage":{"input_tokens":1,"output_tokens":1}}`,
	})

	if err := h.client.Send(golemclient.UserMessage{
		Type: golemclient.TypeUserMessage, Session: id, Text: "keep going",
	}); err != nil {
		t.Fatalf("send user_message: %v", err)
	}

	// The turn must survive the failed compaction and complete.
	failed := h.expectType(golemclient.TypeCompactFailed).(golemclient.CompactFailed)
	if failed.Session != id {
		t.Errorf("compact_failed session = %q, want %q", failed.Session, id)
	}
	if !strings.Contains(failed.Reason, "auto compaction skipped") {
		t.Errorf("compact_failed reason = %q, want the auto-compaction prefix", failed.Reason)
	}

	h.expectType(golemclient.TypeTurnEnd)

	// History untouched: no barrier, both turns intact.
	sess, err := h.store.Get(id)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}

	if len(sess.Messages) != 4 {
		t.Fatalf("messages = %d, want 4 (un-compacted turns), got:\n%+v",
			len(sess.Messages), sess.Messages)
	}
}

// TestAutoCompact_DisabledByZeroThreshold asserts AutoCompactBytes = 0
// turns the trigger off entirely.
func TestAutoCompact_DisabledByZeroThreshold(t *testing.T) {
	t.Parallel()

	provider := &compactProvider{
		scriptedProvider: scriptedProvider{rounds: [][]llm.Event{compactTurnEvents}},
	}
	h := newHarnessWithConfig(t, provider, server.Config{AutoCompactBytes: 0})
	id := h.createSessionTracked()

	appendWire(t, h, id, []string{
		`{"type":"user_message","text":"` + giantText("q") + `"}`,
		`{"type":"text_delta","delta":"` + giantText("a") + `"}`,
		`{"type":"turn_end","usage":{"input_tokens":1,"output_tokens":1}}`,
	})

	h.runTurn(t, "still no compaction")

	if provider.sawSummary {
		t.Error("summary side call ran with auto compaction disabled")
	}
}

// appendWire writes raw JSONL lines straight into the session file via
// the store, bypassing the protocol — the fold's input is identical.
func appendWire(t *testing.T, h *harness, id string, lines []string) {
	t.Helper()

	for _, line := range lines {
		if err := h.store.Append(id, json.RawMessage(line)); err != nil {
			t.Fatalf("append wire line: %v", err)
		}
	}
}

// TestAutoCompact_InterruptCancelsSummarySideCall reproduces the read-loop
// stall (A1): autoCompact ran synchronously on the connection's scanner
// loop, so an interrupt arriving behind a slow summary side call was not
// read until the call finished (up to compactTimeout) — and the summary
// call built its context from Background, so nothing cancelled it. The
// turn pipeline (barrier + user message + agent loop) must run in the
// turn goroutine, and the summary call must derive from the turn context.
func TestAutoCompact_InterruptCancelsSummarySideCall(t *testing.T) {
	t.Parallel()

	summaryStarted := make(chan struct{})
	summaryCtx := make(chan context.Context, 1)
	provider := &compactProvider{
		scriptedProvider: scriptedProvider{rounds: [][]llm.Event{compactTurnEvents}},
	}
	provider.summaryOverride = func(ctx context.Context) (<-chan llm.Event, error) {
		select {
		case summaryStarted <- struct{}{}:
		default:
		}
		summaryCtx <- ctx

		out := make(chan llm.Event, 1)
		go func() {
			defer close(out)
			<-ctx.Done()
			out <- llm.Event{Type: llm.EventError, Err: ctx.Err()}
		}()

		return out, nil
	}

	h := newHarnessWithConfig(t, provider, server.Config{
		AutoCompactBytes: server.DefaultAutoCompactBytes,
	})
	id := h.createSessionTracked()

	appendWire(t, h, id, []string{
		`{"type":"user_message","text":"` + giantText("q") + `"}`,
		`{"type":"text_delta","delta":"` + giantText("a") + `"}`,
		`{"type":"turn_end","usage":{"input_tokens":1,"output_tokens":1}}`,
	})

	if err := h.client.Send(golemclient.UserMessage{
		Type: golemclient.TypeUserMessage, Session: id, Text: "one more",
	}); err != nil {
		t.Fatalf("send user_message: %v", err)
	}

	select {
	case <-summaryStarted:
	case <-time.After(5 * time.Second):
		t.Fatal("summary side call never started")
	}

	// The interrupt must reach the turn while the summary call is still
	// in flight: it is queued behind nothing but the read loop.
	if err := h.client.Send(golemclient.Interrupt{
		Type: golemclient.TypeInterrupt, Session: id,
	}); err != nil {
		t.Fatalf("send interrupt: %v", err)
	}

	// Cancellation must kill the summary side call within the test's
	// patience, not compactTimeout (60s).
	select {
	case ctx := <-summaryCtx:
		select {
		case <-ctx.Done():
		case <-time.After(5 * time.Second):
			t.Fatal("summary side call context was never cancelled by the interrupt")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("summary side call context never handed over")
	}

	// The killed side call surfaces as compact_failed (ADR-0025: a failed
	// compaction never blocks the turn), and the cancelled turn's tail
	// error follows without the connection stalling.
	if failed, ok := h.expectTypeTolerant(golemclient.TypeCompactFailed).(golemclient.CompactFailed); ok {
		if failed.Session != id {
			t.Errorf("compact_failed session = %q, want %q", failed.Session, id)
		}
	}

	// Drain the cancelled turn's terminal event before cleanup: the turn
	// goroutine may still be appending its tail when the test body ends,
	// and t.TempDir's RemoveAll then races the write on slow runners
	// ("directory not empty", CI run 38742622506). Observing the terminal
	// event means every store append of this turn already happened —
	// persist-before-emit guarantees it.
	h.drainTerminal()
}

// drainTerminal consumes events until the turn's terminal event
// (turn_end or error) or a short patience elapses. It is the cleanup
// handshake for tests that leave a cancelled turn's tail in flight:
// once the terminal event is observed, the turn goroutine has finished
// every store append (persist-before-emit), so temp-dir teardown cannot
// race a write.
func (h *harness) drainTerminal() {
	h.t.Helper()

	const patience = 5 * time.Second
	deadline := time.Now().Add(patience)

	for time.Now().Before(deadline) {
		ev, got, err := h.client.ReceiveWithTimeout(500 * time.Millisecond)
		if err != nil || !got {
			continue
		}

		switch ev.(type) {
		case golemclient.TurnEnd, golemclient.ErrorEvent:
			return
		default:
			// interim events (deltas, compact_failed, title) keep flowing
		}
	}
}
