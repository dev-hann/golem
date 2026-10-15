package golemclient_test

import (
	"bufio"
	"bytes"
	"encoding/json"
	"net"
	"testing"

	"github.com/dev-hann/golem/pkg/golemclient"
)

func TestRoundTrip_AllClientEvents(t *testing.T) {
	t.Parallel()
	events := []any{
		golemclient.UserMessage{Type: golemclient.TypeUserMessage, Session: "s1", Text: "hello"},
		golemclient.Interrupt{Type: golemclient.TypeInterrupt, Session: "s1", Turn: "t1"},
		golemclient.CreateSession{Type: golemclient.TypeCreateSession, Cwd: "/tmp"},
		golemclient.ListSessions{Type: golemclient.TypeListSessions},
		golemclient.LoadSession{Type: golemclient.TypeLoadSession, Session: "s1"},
		golemclient.RenameSession{Type: golemclient.TypeRenameSession, Session: "s1", Title: "t"},
		golemclient.DeleteSession{Type: golemclient.TypeDeleteSession, Session: "s1"},
	}
	for _, ev := range events {
		t.Run(jsonTypeName(t, ev), func(t *testing.T) {
			t.Parallel()
			roundTrip(t, ev)
		})
	}
}

func TestRoundTrip_AllServerEvents(t *testing.T) {
	t.Parallel()
	events := []any{
		golemclient.SessionMeta{
			Type: golemclient.TypeSessionMeta, Session: "s1", Cwd: "/tmp",
			Created: "2026-09-27T10:00:00Z", Turns: 3,
		},
		golemclient.History{
			Type: golemclient.TypeHistory, Session: "s1",
			Events: []json.RawMessage{json.RawMessage(`{"type":"text_delta","delta":"hi"}`)},
		},
		golemclient.SessionList{Type: golemclient.TypeSessionList, Sessions: []golemclient.SessionSummary{
			{ID: "s1", Cwd: "/tmp", Created: "now", Turns: 1, LastActive: "now"},
			{ID: "s2", Cwd: "/tmp", Created: "now", Turns: 1, LastActive: "now", Busy: true},
		}},
		golemclient.TurnStart{
			Type: golemclient.TypeTurnStart, Session: "s1", Turn: "t1",
			Provider: "anthropic", Model: "m",
		},
		golemclient.TextDelta{Type: golemclient.TypeTextDelta, Session: "s1", Turn: "t1", Delta: "chunk"},
		golemclient.ToolCallStart{
			Type: golemclient.TypeToolCallStart, Session: "s1", Turn: "t1",
			CallID: "c1", Tool: "read", Args: json.RawMessage(`{"path":"f"}`),
		},
		golemclient.ToolCallEnd{
			Type: golemclient.TypeToolCallEnd, Session: "s1", Turn: "t1",
			CallID: "c1", OK: true, Result: "contents",
		},
		golemclient.ErrorEvent{
			Type: golemclient.TypeError, Session: "s1", Turn: "t1",
			Code: "provider_error", Message: "boom",
		},
		golemclient.TurnEnd{
			Type: golemclient.TypeTurnEnd, Session: "s1", Turn: "t1",
			Usage: golemclient.Usage{InputTokens: 5, OutputTokens: 2},
		},
		golemclient.SessionDeleted{Type: golemclient.TypeSessionDeleted, Session: "s1"},
	}
	for _, ev := range events {
		t.Run(jsonTypeName(t, ev), func(t *testing.T) {
			t.Parallel()
			roundTrip(t, ev)
		})
	}
}

// Round-trip for the transient compaction announcement (ADR-0037).
func TestRoundTrip_Compacting(t *testing.T) {
	t.Parallel()

	roundTrip(t, golemclient.Compacting{
		Type: golemclient.TypeCompacting, Session: "s1",
	})
}

func TestDecode_UnknownTypeReturnsEnvelope(t *testing.T) {
	t.Parallel()
	got, err := golemclient.Decode([]byte(`{"type":"future_thing","session":"s1"}`))
	if err != nil {
		t.Fatalf("Decode: %v", err)
	}
	env, ok := got.(golemclient.Envelope)
	if !ok || env.Type != "future_thing" || env.Session != "s1" {
		t.Errorf("got %#v, want envelope", got)
	}
}

func TestDecode_MalformedJSONErrors(t *testing.T) {
	t.Parallel()
	if _, err := golemclient.Decode([]byte(`{not json`)); err == nil {
		t.Error("expected error for malformed JSON")
	}
}

func TestClient_SendReceiveOverPipe(t *testing.T) {
	t.Parallel()
	clientConn, serverConn := net.Pipe()
	defer serverConn.Close()
	client := golemclient.NewClient(clientConn)
	defer client.Close() //nolint:errcheck // pipe close

	go func() {
		// Consume the client's message first: net.Pipe is synchronous, so
		// both sides must read and write concurrently.
		scanner := bufio.NewScanner(serverConn)
		if scanner.Scan() {
			line, _ := json.Marshal(golemclient.TextDelta{
				Type: golemclient.TypeTextDelta, Session: "s1", Turn: "t1", Delta: "hi",
			})
			_, _ = serverConn.Write(append(line, '\n'))
		}
	}()
	if err := client.Send(golemclient.UserMessage{
		Type: golemclient.TypeUserMessage, Session: "s1", Text: "yo",
	}); err != nil {
		t.Fatalf("Send: %v", err)
	}
	ev, err := client.Receive()
	if err != nil {
		t.Fatalf("Receive: %v", err)
	}
	delta, ok := ev.(golemclient.TextDelta)
	if !ok || delta.Delta != "hi" {
		t.Errorf("got %#v, want text delta", ev)
	}
}

// roundTrip marshals ev, decodes the line, and asserts the result equals ev.
func roundTrip(t *testing.T, ev any) {
	t.Helper()
	line, err := json.Marshal(ev)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	got, err := golemclient.Decode(line)
	if err != nil {
		t.Fatalf("Decode: %v", err)
	}
	want, _ := json.Marshal(ev)
	gotJSON, _ := json.Marshal(got)
	if !bytes.Equal(gotJSON, want) {
		t.Errorf("round trip mismatch:\ngot  %s\nwant %s", gotJSON, want)
	}
}

// jsonTypeName extracts a stable subtest name from an event's type field.
func jsonTypeName(t *testing.T, ev any) string {
	t.Helper()
	data, err := json.Marshal(ev)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var env golemclient.Envelope
	if err := json.Unmarshal(data, &env); err != nil {
		t.Fatalf("unmarshal envelope: %v", err)
	}
	return env.Type
}
