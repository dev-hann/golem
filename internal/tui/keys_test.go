package tui

import (
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/dev-hann/golem/pkg/golemclient"
)

func TestApplyEvent_IgnoresStaleSessionEvents(t *testing.T) {
	t.Parallel()

	m, _ := wired(t)
	m = deliver(t, m, golemclient.SessionMeta{Type: golemclient.TypeSessionMeta, Session: "s1"})

	// A delta from another session must not touch this transcript.
	m = deliver(t, m, golemclient.TextDelta{
		Type: golemclient.TypeTextDelta, Session: "stale", Delta: "ghost text",
	})

	for _, b := range m.blocks {
		if strings.Contains(b.text, "ghost text") {
			t.Fatalf("stale delta leaked into transcript: %+v", m.blocks)
		}
	}

	// Current-session deltas still land.
	m = deliver(t, m, golemclient.TextDelta{
		Type: golemclient.TypeTextDelta, Session: "s1", Delta: "live text",
	})

	if !strings.Contains(m.View(), "live text") {
		t.Errorf("current-session delta missing from view")
	}
}

func TestKey_LeaderN_StartsNewSession(t *testing.T) {
	t.Parallel()

	m, fc := wired(t)
	m = deliver(t, m, golemclient.SessionMeta{Type: golemclient.TypeSessionMeta, Session: "s1"})
	m = deliver(t, m, golemclient.TurnStart{
		Type: golemclient.TypeTurnStart, Session: "s1", Provider: "zai-coding", Model: "glm-5.3",
	})
	m = deliver(t, m, golemclient.TextDelta{
		Type: golemclient.TypeTextDelta, Session: "s1", Delta: "old session text",
	})

	m = arm(m)
	m = key(t, m, rkey('n'))

	if created, ok := lastSent(t, fc).(golemclient.CreateSession); !ok {
		t.Fatalf("sent = %#v, want CreateSession", lastSent(t, fc))
	} else if created.Type != golemclient.TypeCreateSession {
		t.Errorf("created = %#v", created)
	}

	if len(m.blocks) != 0 {
		t.Errorf("blocks after leader n = %+v, want empty", m.blocks)
	}

	if m.turnActive {
		t.Error("leader n must clear the turn flag locally")
	}

	// The new session's meta re-arms the client; stale s1 streaming is dropped.
	m = deliver(t, m, golemclient.SessionMeta{Type: golemclient.TypeSessionMeta, Session: "s2"})
	m = deliver(t, m, golemclient.TextDelta{
		Type: golemclient.TypeTextDelta, Session: "s1", Delta: "stale tail",
	})

	if strings.Contains(m.View(), "stale tail") || strings.Contains(m.View(), "old session text") {
		t.Errorf("stale content leaked after new session:\n%s", m.View())
	}

	if m.sessionID != "s2" {
		t.Errorf("sessionID = %q, want s2", m.sessionID)
	}
}

func TestKey_LeaderL_OpensSessionPicker(t *testing.T) {
	t.Parallel()

	m, fc := wired(t)
	m = deliver(t, m, golemclient.SessionMeta{Type: golemclient.TypeSessionMeta, Session: "s1"})

	m = arm(m)
	m = key(t, m, rkey('l'))

	if _, ok := lastSent(t, fc).(golemclient.ListSessions); !ok {
		t.Fatalf("sent = %#v, want ListSessions", lastSent(t, fc))
	}

	m = deliver(t, m, golemclient.SessionList{
		Type:     golemclient.TypeSessionList,
		Sessions: []golemclient.SessionSummary{{ID: "s1", Cwd: "/w", Turns: 3}},
	})

	if m.mode != modeSessions {
		t.Errorf("mode = %v, want sessions picker", m.mode)
	}
}

func TestKey_LeaderQ_Quits(t *testing.T) {
	t.Parallel()

	m, _ := wired(t)

	m = arm(m)
	m = key(t, m, rkey('q'))

	if !m.quitting {
		t.Error("leader q must quit")
	}
}

func TestKey_CtrlD_Quits(t *testing.T) {
	t.Parallel()

	m, _ := wired(t)

	m = key(t, m, tea.KeyMsg{Type: tea.KeyCtrlD})

	if !m.quitting {
		t.Error("ctrl+d must quit")
	}
}

// RED first: the exit-confirmation ladder. First ctrl+c on an idle empty
// composer must ASK, not quit; a second press within the window quits.
func TestKey_CtrlC_ConfirmExitLadder(t *testing.T) {
	t.Parallel()

	m, _ := wired(t)

	m = pressKey(t, m, tea.KeyMsg{Type: tea.KeyCtrlC})

	if m.quitting {
		t.Fatal("first ctrl+c on an empty composer must not quit — it must ask")
	}

	if !m.exitArmed {
		t.Error("first ctrl+c must arm the exit confirmation")
	}

	if !strings.Contains(m.input.Placeholder, "ctrl+c") {
		t.Errorf("the composer must show the quit hint: %q", m.input.Placeholder)
	}

	// Any other key disarms it (an arrow moves the cursor, types nothing).
	m = pressKey(t, m, tea.KeyMsg{Type: tea.KeyUp})

	if m.exitArmed {
		t.Error("typing must disarm the exit confirmation")
	}

	// Arm again, let the window lapse.
	m = pressKey(t, m, tea.KeyMsg{Type: tea.KeyCtrlC})
	m = exitTick(t, m, m.exitGen)
	m = pressKey(t, m, tea.KeyMsg{Type: tea.KeyUp})

	if m.quitting {
		t.Error("an expired confirmation must not quit on the next key")
	}

	if m.exitArmed {
		t.Error("an expired confirmation must disarm")
	}

	// Arm again; the second press within the window quits.
	m = pressKey(t, m, tea.KeyMsg{Type: tea.KeyCtrlC})
	m = pressKey(t, m, tea.KeyMsg{Type: tea.KeyCtrlC})

	if !m.quitting {
		t.Error("second ctrl+c within the window must quit")
	}
}

// pressKey drives one key press WITHOUT running the resulting cmd — the
// exit arm returns a tea.Tick that would stall the test for the window.
func pressKey(t *testing.T, m Model, k tea.KeyMsg) Model {
	t.Helper()
	next, _ := m.Update(k)
	return next.(Model)
}

// The armed state must reset when the world changes under it: a submit
// (turn starting) or a draft appearing.
func TestKey_CtrlC_ArmedStateResetsOnSubmit(t *testing.T) {
	t.Parallel()

	m, fc := wired(t)
	m = deliver(t, m, golemclient.SessionMeta{Type: golemclient.TypeSessionMeta, Session: "s1"})
	m = resize(m, 100, 40)

	m = pressKey(t, m, tea.KeyMsg{Type: tea.KeyCtrlC})

	if !m.exitArmed {
		t.Fatal("precondition: exit armed")
	}

	m = key(t, m, tea.KeyMsg{Type: tea.KeyEnter})

	if m.exitArmed {
		t.Error("submitting must disarm the exit confirmation")
	}

	if len(fc.sent) != 0 {
		t.Errorf("submit with an empty composer must send nothing: %+v", fc.sent)
	}
}

func TestKey_CtrlC_EscDisarms(t *testing.T) {
	t.Parallel()

	m, _ := wired(t)

	m = pressKey(t, m, tea.KeyMsg{Type: tea.KeyCtrlC})
	m = pressKey(t, m, tea.KeyMsg{Type: tea.KeyEscape})

	if m.exitArmed {
		t.Error("esc must disarm the exit confirmation")
	}
}

func TestKey_CtrlC_LadderOrderUnchanged(t *testing.T) {
	t.Parallel()

	m, fc := wired(t)
	m = deliver(t, m, golemclient.TurnStart{Type: golemclient.TypeTurnStart, Session: "s1"})

	// A running turn outranks the exit arm: ctrl+c interrupts first.
	m = pressKey(t, m, tea.KeyMsg{Type: tea.KeyCtrlC})
	next, sendCmd := m.Update(tea.KeyMsg{Type: tea.KeyCtrlC})
	m = next.(Model)
	if sendCmd != nil {
		_ = sendCmd()
	}

	if _, ok := lastSent(t, fc).(golemclient.Interrupt); !ok {
		t.Fatalf("ctrl+c during a turn must interrupt, sent=%v", fc.sent)
	}

	if m.exitArmed {
		t.Error("an interrupting press must not arm the exit confirmation")
	}
}

func TestKey_CtrlC_ClearsInputBeforeQuitting(t *testing.T) {
	t.Parallel()

	m, fc := wired(t)
	m.input.SetValue("draft")

	m = key(t, m, tea.KeyMsg{Type: tea.KeyCtrlC})

	if got := m.input.Value(); got != "" {
		t.Errorf("first ctrl+c must clear the composer, got %q", got)
	}

	if m.quitting {
		t.Error("first ctrl+c with draft text must not quit")
	}

	if len(fc.sent) != 0 {
		t.Errorf("ctrl+c clearing input sent events: %+v", fc.sent)
	}

	m = key(t, m, tea.KeyMsg{Type: tea.KeyCtrlC}) // first press arms the confirm…

	if m.quitting {
		t.Error("first ctrl+c on an empty composer must ask, not quit")
	}

	m = key(t, m, tea.KeyMsg{Type: tea.KeyCtrlC}) // …the second quits

	if !m.quitting {
		t.Error("ctrl+c on an empty composer must quit after the confirm")
	}
}

func TestKey_Esc_InterruptsActiveTurn(t *testing.T) {
	t.Parallel()

	m, fc := wired(t)
	m = deliver(t, m, golemclient.SessionMeta{Type: golemclient.TypeSessionMeta, Session: "s1"})
	m = deliver(t, m, golemclient.TurnStart{Type: golemclient.TypeTurnStart, Session: "s1"})

	_ = key(t, m, tea.KeyMsg{Type: tea.KeyEscape})

	if interrupted, ok := lastSent(t, fc).(golemclient.Interrupt); !ok || interrupted.Session != "s1" {
		t.Fatalf("sent = %#v, want Interrupt for s1", lastSent(t, fc))
	}
}

func TestKey_Esc_IdleDoesNotQuit(t *testing.T) {
	t.Parallel()

	m, _ := wired(t)
	m = deliver(t, m, golemclient.SessionMeta{Type: golemclient.TypeSessionMeta, Session: "s1"})

	_ = key(t, m, tea.KeyMsg{Type: tea.KeyEscape})

	if m.quitting {
		t.Error("esc while idle must not quit — that stays ctrl+c's job")
	}
}

func TestPlaceholderDocumentsShortcuts(t *testing.T) {
	t.Parallel()

	m := New("/tmp/unused.sock")

	// The placeholder stays minimal: the bindings reference lives behind
	// ctrl+p — the palette doubles as the cheat sheet.
	for _, want := range []string{"enter", "alt+enter", "ctrl+p"} {
		if !strings.Contains(m.input.Placeholder, want) {
			t.Errorf("placeholder missing %q: %s", want, m.input.Placeholder)
		}
	}
}

func TestKey_CtrlP_OpensCommandPalette(t *testing.T) {
	t.Parallel()

	m, _ := wired(t)
	m = resize(m, 100, 40)

	m = key(t, m, tea.KeyMsg{Type: tea.KeyCtrlP})

	if m.mode != modeCommand {
		t.Fatalf("mode = %v, want command palette", m.mode)
	}

	view := m.View()
	for _, want := range []string{
		"commands", "new session", "session list", "model list", "quit",
		"ctrl+x n", "ctrl+x l", "ctrl+x m", "ctrl+d",
	} {
		if !strings.Contains(view, want) {
			t.Errorf("palette missing %q:\n%s", want, view)
		}
	}
}

func TestPalette_TypeToFilterThenEnterRuns(t *testing.T) {
	t.Parallel()

	m, _ := wired(t)
	m = resize(m, 100, 40)
	m = key(t, m, tea.KeyMsg{Type: tea.KeyCtrlP})

	for _, r := range "mod" {
		m = key(t, m, rkey(r))
	}

	view := m.View()
	if !strings.Contains(view, "model list") {
		t.Fatalf("filter dropped the matching row:\n%s", view)
	}

	if strings.Contains(view, "new session") {
		t.Errorf("filter must drop non-matching rows:\n%s", view)
	}

	m = key(t, m, tea.KeyMsg{Type: tea.KeyEnter})

	if m.mode != modeModels {
		t.Errorf("mode after enter = %v, want models", m.mode)
	}

	if m.cmdQuery != "" || m.cmdCursor != 0 {
		t.Errorf("palette state must reset after run: query=%q cursor=%d", m.cmdQuery, m.cmdCursor)
	}
}

func TestPalette_FuzzySubsequenceMatch(t *testing.T) {
	t.Parallel()

	m, fc := wired(t)
	m = resize(m, 100, 40)
	m = deliver(t, m, golemclient.SessionMeta{Type: golemclient.TypeSessionMeta, Session: "s1"})
	m = key(t, m, tea.KeyMsg{Type: tea.KeyCtrlP})

	// "ns" — n…s in order — matches "new session" and nothing else.
	for _, r := range "ns" {
		m = key(t, m, rkey(r))
	}

	view := m.View()
	if !strings.Contains(view, "new session") {
		t.Fatalf("fuzzy match missing new session:\n%s", view)
	}

	if strings.Contains(view, "model list") {
		t.Errorf("non-subsequence rows must be filtered out:\n%s", view)
	}

	key(t, m, tea.KeyMsg{Type: tea.KeyEnter})

	if _, ok := lastSent(t, fc).(golemclient.CreateSession); !ok {
		t.Fatalf("sent = %#v, want CreateSession", lastSent(t, fc))
	}
}

func TestPalette_CtrlNCtrlP_NavigateAndDialogKeysReuse(t *testing.T) {
	t.Parallel()

	m, fc := wired(t)
	m = resize(m, 100, 40)
	m = key(t, m, tea.KeyMsg{Type: tea.KeyCtrlP})

	m = key(t, m, tea.KeyMsg{Type: tea.KeyCtrlN}) // cursor → session list
	m = key(t, m, tea.KeyMsg{Type: tea.KeyEnter})

	if _, ok := lastSent(t, fc).(golemclient.ListSessions); !ok {
		t.Fatalf("sent = %#v, want ListSessions", lastSent(t, fc))
	}

	m = deliver(t, m, golemclient.SessionList{
		Type: golemclient.TypeSessionList,
		Sessions: []golemclient.SessionSummary{
			{ID: "old", Cwd: "/a", Turns: 1},
			{ID: "new", Cwd: "/b", Turns: 2},
		},
	})

	if m.mode != modeSessions {
		t.Fatalf("mode = %v, want sessions picker", m.mode)
	}

	// The session dialog honors the same ctrl+n/ctrl+p navigation.
	m = key(t, m, tea.KeyMsg{Type: tea.KeyCtrlN}) // pickIndex → 1
	m = key(t, m, tea.KeyMsg{Type: tea.KeyCtrlP}) // pickIndex → 0
	m = key(t, m, tea.KeyMsg{Type: tea.KeyEnter})

	loaded, ok := lastSent(t, fc).(golemclient.LoadSession)
	if !ok || loaded.Session != "old" {
		t.Errorf("load = %#v, want session old (ctrl+p moved back up)", loaded)
	}
}

func TestPalette_NoMatchBackspaceAndEsc(t *testing.T) {
	t.Parallel()

	m, fc := wired(t)
	m = resize(m, 100, 40)
	m = deliver(t, m, golemclient.SessionMeta{Type: golemclient.TypeSessionMeta, Session: "s1"})
	m = key(t, m, tea.KeyMsg{Type: tea.KeyCtrlP})

	m = key(t, m, rkey('z'))

	if !strings.Contains(m.View(), "no matches") {
		t.Errorf("empty result set must say so:\n%s", m.View())
	}

	m = key(t, m, tea.KeyMsg{Type: tea.KeyEnter})

	if len(fc.sent) != 0 {
		t.Errorf("enter with no matches must not run anything: %+v", fc.sent)
	}

	if m.mode != modeCommand {
		t.Errorf("enter with no matches must keep the palette open, mode = %v", m.mode)
	}

	// Backspace empties the query; every command is back.
	m = key(t, m, tea.KeyMsg{Type: tea.KeyBackspace})
	m = key(t, m, tea.KeyMsg{Type: tea.KeyEnter})

	if _, ok := lastSent(t, fc).(golemclient.CreateSession); !ok {
		t.Fatalf("sent = %#v, want CreateSession after clearing the query", lastSent(t, fc))
	}

	m = key(t, m, tea.KeyMsg{Type: tea.KeyCtrlP})
	m = key(t, m, tea.KeyMsg{Type: tea.KeyEscape})

	if m.mode != modeChat {
		t.Errorf("esc must close the palette, mode = %v", m.mode)
	}
}

func TestKey_LeaderMOpensModelModalAndApplies(t *testing.T) {
	t.Parallel()

	m, fc := wired(t)
	m = resize(m, 100, 40)
	m = m.WithModels([]string{"glm-5.3", "glm-5.3-flash", "glm-5.3-air"})
	m = deliver(t, m, golemclient.SessionMeta{Type: golemclient.TypeSessionMeta, Session: "s1"})
	m = deliver(t, m, golemclient.TurnStart{
		Type: golemclient.TypeTurnStart, Session: "s1", Provider: "zai-coding", Model: "glm-5.3",
	})

	m = arm(m)
	m = key(t, m, rkey('m'))

	view := m.View()
	if !strings.Contains(view, "models · zai-coding") {
		t.Fatalf("model modal missing title:\n%s", view)
	}

	for _, want := range []string{"glm-5.3", "glm-5.3-flash", "glm-5.3-air"} {
		if !strings.Contains(view, want) {
			t.Errorf("model modal missing %q:\n%s", want, view)
		}
	}

	m = key(t, m, tea.KeyMsg{Type: tea.KeyDown})
	m = key(t, m, tea.KeyMsg{Type: tea.KeyEnter})

	sent, ok := lastSent(t, fc).(golemclient.SetModel)
	if !ok || sent.Model != "glm-5.3-flash" || sent.Session != "s1" {
		t.Fatalf("sent = %#v, want SetModel glm-5.3-flash for s1", sent)
	}

	if m.mode != modeChat || m.modelName != "glm-5.3-flash" {
		t.Errorf("after apply: mode=%v model=%q", m.mode, m.modelName)
	}
}

func TestKey_LeaderMWithoutModelsShowsCurrentOnly(t *testing.T) {
	t.Parallel()

	m, _ := wired(t)
	m = resize(m, 100, 40)
	m = deliver(t, m, golemclient.TurnStart{
		Type: golemclient.TypeTurnStart, Session: "s1", Provider: "zai-coding", Model: "glm-5.3",
	})

	m = arm(m)
	m = key(t, m, rkey('m'))

	if !strings.Contains(m.View(), "glm-5.3") {
		t.Fatalf("current model missing from fallback modal:\n%s", m.View())
	}

	if !strings.Contains(m.View(), "no model list") {
		t.Errorf("fallback note missing:\n%s", m.View())
	}
}

func TestKey_LeaderUnboundKeyFallsThrough(t *testing.T) {
	t.Parallel()

	m, fc := wired(t)
	m = arm(m)
	m = key(t, m, rkey('z'))

	if m.mode != modeChat {
		t.Errorf("mode = %v, want chat", m.mode)
	}

	if got := m.input.Value(); got != "z" {
		t.Errorf("input = %q, want z — a stray arm must not swallow typing", got)
	}

	if len(fc.sent) != 0 {
		t.Errorf("unbound key sent events: %+v", fc.sent)
	}
}

func TestKey_LeaderEscCancelsWithoutInterrupt(t *testing.T) {
	t.Parallel()

	m, fc := wired(t)
	m = deliver(t, m, golemclient.TurnStart{Type: golemclient.TypeTurnStart, Session: "s1"})

	m = arm(m)
	m = key(t, m, tea.KeyMsg{Type: tea.KeyEscape})

	if m.mode != modeChat {
		t.Errorf("mode = %v, want chat", m.mode)
	}

	for _, sent := range fc.sent {
		if _, ok := sent.(golemclient.Interrupt); ok {
			t.Error("esc during a leader sequence must cancel it, not interrupt the turn")
		}
	}
}

func TestKey_LeaderTimeoutDisarms(t *testing.T) {
	t.Parallel()

	m, fc := wired(t)
	m = arm(m)               // generation 1
	m = tick(m, 1)           // the window elapses
	m = key(t, m, rkey('n')) // a plain key press now, not a command

	if len(fc.sent) != 0 {
		t.Fatalf("sent = %+v, want none — an expired sequence must not fire", fc.sent)
	}

	if got := m.input.Value(); got != "n" {
		t.Errorf("input = %q, want n typed after expiry", got)
	}
}

func TestKey_LeaderStaleTickDoesNotDisarm(t *testing.T) {
	t.Parallel()

	m, _ := wired(t)
	m = arm(m) // generation 1
	m = tick(m, 1)
	m = arm(m)     // generation 2
	m = tick(m, 1) // stale tick from the first arm

	m = key(t, m, rkey('m'))

	if m.mode != modeModels {
		t.Errorf("mode = %v, want models — a stale tick disarmed a live sequence", m.mode)
	}
}

func TestScroll_CtrlGTop_CtrlAltGBottom(t *testing.T) {
	t.Parallel()

	m, _ := wired(t)
	m = resize(m, 80, 24)
	m = deliver(t, m, golemclient.TurnStart{Type: golemclient.TypeTurnStart})
	m = deliver(t, m, golemclient.TextDelta{Type: golemclient.TypeTextDelta, Delta: "head-marker\n"})
	for range 100 {
		m = deliver(t, m, golemclient.TextDelta{
			Type: golemclient.TypeTextDelta, Delta: strings.Repeat("x", 60) + "\n",
		})
	}
	m = deliver(t, m, golemclient.TextDelta{Type: golemclient.TypeTextDelta, Delta: "tail-marker"})

	m = key(t, m, tea.KeyMsg{Type: tea.KeyCtrlG})

	view := m.View()
	if !strings.Contains(view, "head-marker") {
		t.Errorf("ctrl+g must jump to the transcript top:\n%s", view)
	}

	if strings.Contains(view, "tail-marker") {
		t.Errorf("tail must be off-screen after ctrl+g:\n%s", view)
	}

	m = key(t, m, tea.KeyMsg{Type: tea.KeyCtrlG, Alt: true})

	view = m.View()
	if !strings.Contains(view, "tail-marker") {
		t.Errorf("ctrl+alt+g must jump to the transcript bottom:\n%s", view)
	}

	if strings.Contains(view, "head-marker") {
		t.Errorf("head must be off-screen after ctrl+alt+g:\n%s", view)
	}
}
