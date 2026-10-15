package tui

import (
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"

	"github.com/dev-hann/golem/pkg/golemclient"
)

// ADR-0018: a client owns no session until it speaks. connect() dials
// without creating; the first submit triggers create_session and the
// message rides behind the session_meta reply.

func TestConnect_DoesNotCreateSession(t *testing.T) {
	t.Parallel()

	_, fc := wired(t)

	if len(fc.sent) != 0 {
		t.Fatalf("connect must not create a session eagerly, sent: %+v", fc.sent)
	}
}

func TestLazySession_FirstSubmitCreatesThenFlushes(t *testing.T) {
	t.Parallel()

	m, fc := wired(t)
	m = resize(m, 100, 40)

	m.input.SetValue("hello golem")
	m = key(t, m, tea.KeyPressMsg{Code: tea.KeyEnter})

	if created, ok := lastSent(t, fc).(golemclient.CreateSession); !ok || created.Type != golemclient.TypeCreateSession {
		t.Fatalf("sent = %#v, want CreateSession", lastSent(t, fc))
	}

	if len(fc.sent) != 1 {
		t.Fatalf("no user_message before session_meta, sent: %+v", fc.sent)
	}

	if !strings.Contains(m.View().Content, "hello golem") {
		t.Errorf("queued message should render in the transcript:\n%s", m.View().Content)
	}

	if !strings.Contains(m.statusLine(), "creating session") {
		t.Errorf("status should show the pending creation, got %q", m.statusLine())
	}

	m = deliver(t, m, golemclient.SessionMeta{Type: golemclient.TypeSessionMeta, Session: "s1"})

	msg, ok := lastSent(t, fc).(golemclient.UserMessage)
	if !ok || msg.Session != "s1" || msg.Text != "hello golem" {
		t.Fatalf("sent = %#v, want user_message 'hello golem' for s1", lastSent(t, fc))
	}

	if strings.Contains(m.statusLine(), "creating session") {
		t.Errorf("status should drop the pending marker after meta, got %q", m.statusLine())
	}
}

func TestLazySession_SubmitsWhilePendingAllFlushInOrder(t *testing.T) {
	t.Parallel()

	m, fc := wired(t)

	m = key(t, m, rkey('o'))
	m = key(t, m, tea.KeyPressMsg{Code: tea.KeyEnter})
	m.input.SetValue("second")
	m = key(t, m, tea.KeyPressMsg{Code: tea.KeyEnter})

	if len(fc.sent) != 1 {
		t.Fatalf("both submits must queue behind one create_session, sent: %+v", fc.sent)
	}

	m = deliver(t, m, golemclient.SessionMeta{Type: golemclient.TypeSessionMeta, Session: "s9"})

	var texts []string
	for _, ev := range fc.sent {
		if msg, ok := ev.(golemclient.UserMessage); ok {
			texts = append(texts, msg.Text)
		}
	}

	if strings.Join(texts, "|") != "o|second" {
		t.Errorf("flushed messages = %v, want [o second] in order (sent: %+v)", texts, fc.sent)
	}
}

func TestLazySession_LeaderNBeforeSessionIsNoop(t *testing.T) {
	t.Parallel()

	m, fc := wired(t)

	m = arm(m)
	m = key(t, m, rkey('n'))

	if len(fc.sent) != 0 {
		t.Errorf("ctrl+x n before any session must send nothing, sent: %+v", fc.sent)
	}

	if len(m.blocks) != 0 {
		t.Errorf("nothing to reset before a session exists, blocks: %+v", m.blocks)
	}
}

func TestLazySession_ModelPickBeforeSessionDefersSetModel(t *testing.T) {
	t.Parallel()

	m, fc := wired(t)
	m = m.WithModels([]string{"glm-5.3", "glm-5.3-flash"})

	m = arm(m)
	m = key(t, m, rkey('m'))
	m = key(t, m, tea.KeyPressMsg{Code: tea.KeyDown})
	m = key(t, m, tea.KeyPressMsg{Code: tea.KeyEnter})

	if len(fc.sent) != 0 {
		t.Fatalf("set_model needs a session; nothing may send yet, sent: %+v", fc.sent)
	}

	if m.modelName != "glm-5.3-flash" {
		t.Errorf("modelName = %q, want the local choice remembered", m.modelName)
	}

	m.input.SetValue("hi")
	m = key(t, m, tea.KeyPressMsg{Code: tea.KeyEnter})
	m = deliver(t, m, golemclient.SessionMeta{Type: golemclient.TypeSessionMeta, Session: "s1"})

	if len(fc.sent) != 3 {
		t.Fatalf("sent = %+v, want [create_session, set_model, user_message]", fc.sent)
	}

	for i, ev := range fc.sent[1:] {
		var typ string
		switch e := ev.(type) {
		case golemclient.SetModel:
			typ = e.Type
			if e.Session != "s1" || e.Model != "glm-5.3-flash" {
				t.Errorf("set_model = %#v, want glm-5.3-flash for s1", e)
			}
		case golemclient.UserMessage:
			typ = e.Type
			if e.Session != "s1" || e.Text != "hi" {
				t.Errorf("user_message = %#v, want 'hi' for s1", e)
			}
		default:
			t.Fatalf("flush order broken, got %#v first", ev)
		}

		if typ != golemclient.TypeSetModel && typ != golemclient.TypeUserMessage {
			t.Errorf("flush[%d] = %s, want a session event", i, typ)
		}
	}
}
