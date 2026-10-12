package golemclient_test

import (
	"net"
	"testing"
	"time"

	"github.com/dev-hann/golem/pkg/golemclient"
)

func TestClient_ReceiveAfterCloseErrors(t *testing.T) {
	t.Parallel()
	clientConn, serverConn := net.Pipe()
	defer serverConn.Close()
	client := golemclient.NewClient(clientConn)
	_ = client.Close()
	if _, err := client.Receive(); err == nil {
		t.Error("Receive after Close should error")
	}
}

func TestClient_SendAfterCloseErrors(t *testing.T) {
	t.Parallel()
	clientConn, serverConn := net.Pipe()
	defer serverConn.Close()
	client := golemclient.NewClient(clientConn)
	_ = client.Close()
	if err := client.Send(golemclient.ListSessions{Type: golemclient.TypeListSessions}); err == nil {
		t.Error("Send after Close should error")
	}
}

func TestClient_SendUnmarshalableEventErrors(t *testing.T) {
	t.Parallel()
	clientConn, serverConn := net.Pipe()
	defer serverConn.Close()
	client := golemclient.NewClient(clientConn)
	defer client.Close() //nolint:errcheck // pipe close
	if err := client.Send(map[string]any{"bad": make(chan int)}); err == nil {
		t.Error("Send with unmarshalable payload should error")
	}
}

func TestClient_TimeoutDoesNotLoseLateEvents(t *testing.T) {
	t.Parallel()
	clientConn, serverConn := net.Pipe()
	defer serverConn.Close()
	client := golemclient.NewClient(clientConn)
	defer client.Close() //nolint:errcheck // pipe close

	go func() {
		time.Sleep(150 * time.Millisecond)
		_, _ = serverConn.Write([]byte(`{"type":"session_meta","session":"s1","cwd":"/","created":"now","turns":0}` + "\n"))
	}()
	ev, got, err := client.ReceiveWithTimeout(50 * time.Millisecond)
	if got || err != nil {
		t.Fatalf("expected timeout, got ev=%v got=%v err=%v", ev, got, err)
	}
	after, err := client.Receive() // the late event must still arrive
	if err != nil {
		t.Fatalf("Receive: %v", err)
	}
	if meta, ok := after.(golemclient.SessionMeta); !ok || meta.Session != "s1" {
		t.Errorf("late event lost, got %#v", after)
	}
}
