package tui

import (
	"testing"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/dev-hann/golem/pkg/golemclient"
)

// forkReady wires a model standing in a titled session with one
// completed turn, then selects the palette's fork command.
func forkReady(t *testing.T) (Model, *fakeClient) {
	t.Helper()

	m, fc := wired(t)
	m = deliver(t, m, golemclient.SessionMeta{
		Type: golemclient.TypeSessionMeta, Session: "01PARENT0000000000000000000",
		Cwd: "/proj", Turns: 2,
	})

	return m, fc
}

// update drives one Update cycle and returns the cmd (unlike deliver,
// which drops it).
func update(t *testing.T, m Model, msg tea.Msg) (Model, tea.Cmd) {
	t.Helper()

	next, cmd := m.Update(msg)
	return next.(Model), cmd
}

// runPaletteCommand picks the first command whose name matches.
func runPaletteCommand(t *testing.T, m Model, name string) (Model, tea.Cmd) {
	t.Helper()

	for _, c := range m.commands() {
		if c.name == name {
			next, cmd := c.run(m)
			return next.(Model), cmd
		}
	}

	t.Fatalf("palette command %q not found", name)
	return m, nil
}

func TestForkCommand_SwitchesToForkedSession(t *testing.T) {
	t.Parallel()

	m, fc := forkReady(t)

	next, cmd := runPaletteCommand(t, m, "fork session")
	if cmd == nil {
		t.Fatal("fork session command returned nil cmd")
	}
	_ = cmd()

	forkSend, ok := lastSent(t, fc).(golemclient.ForkSession)
	if !ok {
		t.Fatalf("last sent = %#v, want ForkSession", fc.sent)
	}

	if forkSend.Session != "01PARENT0000000000000000000" || forkSend.Turn != "" {
		t.Errorf("fork send = %+v, want parent id and empty turn (last completed)", forkSend)
	}

	// The confirmation switches the client to the child: the load lands
	// and the transcript rebuilds from the child's history.
	m, _ = update(t, next, serverEventMsg{ev: golemclient.SessionForked{
		Type:    golemclient.TypeSessionForked,
		Session: "01CHILD00000000000000000000", Parent: forkSend.Session,
		Title: "first question (fork #1)",
	}})

	loaded, ok := lastSent(t, fc).(golemclient.LoadSession)
	if !ok {
		t.Fatalf("last sent = %#v, want LoadSession for the child", fc.sent)
	}

	if loaded.Session != "01CHILD00000000000000000000" {
		t.Errorf("load = %+v, want the child id", loaded)
	}

	if m.sessionID != "01CHILD00000000000000000000" {
		t.Errorf("sessionID = %q, want the child id (switched)", m.sessionID)
	}
}

func TestForkCommand_NoSessionIsNoOp(t *testing.T) {
	t.Parallel()

	m, fc := wired(t) // lazy start: no session yet (ADR-0018)

	next, cmd := runPaletteCommand(t, m, "fork session")
	if cmd != nil {
		t.Fatal("fork with no session should be a no-op")
	}

	if len(fc.sent) != 0 {
		t.Fatalf("sent = %v, want nothing", fc.sent)
	}

	if next.sessionID != "" {
		t.Errorf("sessionID = %q, want empty", next.sessionID)
	}
}
