package server_test

import (
	"testing"
	"time"

	"github.com/dev-hann/golem/internal/llm"
	"github.com/dev-hann/golem/pkg/golemclient"
)

// turnRaceRounds is how many back-to-back turns the teardown-race test
// fires; enough iterations to give the microseconds-wide window a real
// chance of being hit on any runner.
const turnRaceRounds = 50

// The turn teardown race (CI failures 2026-10-13/15, runs 38349154511,
// 38359394864, 38739797684): runTurn emitted the terminal turn event
// (turn_end / error) *before* the deferred endTurn unregistered the
// busy state. A client that reacts to turn_end by immediately sending
// the next request hit the still-busy window and got rejected:
//
//	TestCompact_FailedSideCallEmitsCompactFailed — compact_session
//	  answered "a turn is in progress" right after turn_end
//	TestServer_InterruptEndsTurnGracefully — the follow-up user_message
//	  after an interrupt-driven error hit the same window
//
// The protocol contract (PROTOCOL.md): once a client observes turn_end,
// the session must accept its next request. This test pins that by
// racing the teardown repeatedly — each iteration fires the next request
// the instant turn_end lands, widening the statistical catch of the
// teardown window (it is only microseconds wide, but real: three CI
// runs tripped over it).

func TestTurnEndToNextRequest_NoBusyWindow(t *testing.T) {
	t.Parallel()

	// Every round yields one delta — an empty round would end the turn
	// with a provider error and mask the race under test.
	rounds := make([][]llm.Event, turnRaceRounds)
	for i := range rounds {
		rounds[i] = []llm.Event{{Type: llm.EventTextDelta, Delta: "a"}}
	}

	provider := &compactProvider{
		scriptedProvider: scriptedProvider{rounds: rounds},
		summaryEvents:    []llm.Event{{Type: llm.EventTextDelta, Delta: "THE SUMMARY"}},
	}
	h := newHarness(t, provider)
	sess := h.createSessionTracked()

	// turnRaceRounds back-to-back turns: the request for turn i+1 is
	// sent the moment turn i's turn_end is observed.
	for i := range turnRaceRounds {
		if err := h.client.Send(golemclient.UserMessage{
			Type: golemclient.TypeUserMessage, Session: sess, Text: "q",
		}); err != nil {
			t.Fatalf("send user_message %d: %v", i, err)
		}

		for {
			ev, got, err := h.client.ReceiveWithTimeout(5 * time.Second)
			if err != nil || !got {
				t.Fatalf("turn %d: receive: %v", i, err)
			}

			if e, ok := ev.(golemclient.ErrorEvent); ok {
				t.Fatalf("turn %d: request rejected from the teardown window: %+v", i, e)
			}

			if _, ok := ev.(golemclient.TurnEnd); ok {
				break // next request fires immediately — the race
			}
		}
	}

	// The same window must not swallow a compact issued right after the
	// final turn_end (the exact CI failure in run 38739797684).
	if err := h.client.Send(golemclient.CompactSession{
		Type: golemclient.TypeCompactSession, Session: sess,
	}); err != nil {
		t.Fatalf("send compact_session: %v", err)
	}

	compacted, ok := h.expectTypeTolerant(golemclient.TypeSessionCompacted).(golemclient.SessionCompacted)
	if !ok || compacted.Summary != "THE SUMMARY" {
		t.Errorf("expected session_compacted with THE SUMMARY, got %+v", compacted)
	}
}
