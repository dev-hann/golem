// Key handling, split by input mode: the top dispatch, the exit
// confirmation, the chat composer (submit, leader sequences, newline),
// and the wire send helper.

package tui

import (
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/dev-hann/golem/pkg/golemclient"
)

// handleKey dispatches key input by mode.
func (m Model) handleKey(key tea.KeyMsg) (tea.Model, tea.Cmd) {
	if key.Type == tea.KeyCtrlC {
		return m.ctrlC()
	}

	switch m.mode {
	case modeChat:
		return m.updateChat(key)
	case modeSessions:
		return m.updateSessions(key)
	case modeCommand:
		return m.updateCommand(key)
	case modeModels:
		return m.updateModels(key)
	default:
		return m, nil
	}
}

// exitConfirmTimeout bounds how long a lone ctrl+c stays a quit request.
const exitConfirmTimeout = 3 * time.Second

// armExitConfirm arms the quit confirmation: the placeholder swaps to the
// confirm hint for the window's duration.
func armExitConfirm(gen int) tea.Cmd {
	return tea.Tick(exitConfirmTimeout, func(time.Time) tea.Msg {
		return exitTimeoutMsg{gen: gen}
	})
}

// setExit arms or disarms the ctrl+c quit confirmation, keeping the
// composer hint in sync — the placeholder only prints while the input is
// empty, which the arming path guarantees (ctrl+c armed only on empty).
func (m *Model) setExit(armed bool) {
	m.exitArmed = armed

	if armed {
		m.input.Placeholder = exitPlaceholder

		return
	}

	m.input.Placeholder = chatPlaceholder
}

// ctrlC mirrors opencode's exit ladder: a live selection copies first,
// an active turn interrupts, then the composer clears, and only a second
// ctrl+c on an already-empty composer quits — the first press asks.
func (m Model) ctrlC() (tea.Model, tea.Cmd) {
	// A live selection makes ctrl+c copy first; the exit ladder resumes
	// once the selection is gone (opencode's split).
	if text := m.selectedText(); text != "" {
		m.clearSelection()
		m.refreshView()

		return m, m.copyText(text)
	}

	if m.turnActive && m.client != nil {
		return m, m.send(golemclient.Interrupt{
			Type: golemclient.TypeInterrupt, Session: m.sessionID,
		})
	}

	if m.input.Value() != "" {
		m.input.Reset()
		m.syncInputHeight()

		return m, nil
	}

	// Empty idle composer: the first press asks, the second quits.
	if m.exitArmed {
		return m.quitCmd()
	}

	m.setExit(true)
	m.exitGen++

	return m, armExitConfirm(m.exitGen)
}

// quitCmd flags the quit and returns the terminal quit command.
func (m Model) quitCmd() (tea.Model, tea.Cmd) {
	m.quitting = true

	return m, tea.Quit
}

// updateChat handles chat-mode keys.
func (m Model) updateChat(key tea.KeyMsg) (tea.Model, tea.Cmd) {
	if m.leaderArmed {
		return m.leaderKey(key)
	}

	// A pending quit confirmation dies on the next key of any kind — the
	// ladder must not swallow typing (ctrl+c itself already handled).
	if m.exitArmed && key.Type != tea.KeyCtrlC {
		m.setExit(false)
	}

	if m.scrollKey(key) {
		return m, nil
	}

	switch key.Type { //nolint:exhaustive // foreign enum; default routes to the textarea
	case tea.KeyCtrlX:
		m.setLeader(true)
		m.leaderGen++

		return m, armLeader(m.leaderGen)
	case tea.KeyCtrlP:
		m.mode = modeCommand
		m.cmdQuery, m.cmdCursor = "", 0

		return m, nil
	case tea.KeyCtrlD:
		return m.quitCmd()
	case tea.KeyCtrlJ:
		m.insertNewline()

		return m, nil
	case tea.KeyEscape:
		return m.escKey()
	case tea.KeyEnter:
		if key.Alt { // alt/option+enter inserts a newline; plain enter submits
			m.insertNewline()

			return m, nil
		}

		line := strings.TrimSpace(m.input.Value())
		if line == "" {
			return m, nil
		}

		m.input.Reset()
		m.syncInputHeight()

		return m.submit(line)
	default:
		var cmd tea.Cmd

		m.input, cmd = m.input.Update(key)
		m.syncInputHeight()

		return m, cmd
	}
}

// insertNewline adds a line break to the composer (alt+enter / ctrl+j).
func (m *Model) insertNewline() {
	m.input.InsertString("\n")
	m.syncInputHeight()
}

// Composer hint texts: the idle hint explains the input keys, the armed
// hint points at the key-list reference while a ctrl+x sequence pends,
// and the exit hint demands the confirming press.
const (
	chatPlaceholder   = "message… (enter send · alt+enter newline · ctrl+p commands)"
	leaderPlaceholder = "shortcut mode — n new · l sessions · m model · q quit · esc cancels"
	exitPlaceholder   = "quit? press ctrl+c again to exit · any key cancels"
)

// setLeader arms or disarms the ctrl+x prefix mode, keeping the composer's
// hint text in sync — the placeholder only prints while the input is
// empty; the accent left rail (layout) marks the mode regardless.
func (m *Model) setLeader(armed bool) {
	m.leaderArmed = armed

	if armed {
		m.input.Placeholder = leaderPlaceholder

		return
	}

	m.input.Placeholder = chatPlaceholder
}

// newSessionCmd resets the local view and asks the server for a fresh
// session over the same connection. With no session yet there is nothing
// to reset or create (ADR-0018) — the first message owns that create.
func (m Model) newSessionCmd() (tea.Model, tea.Cmd) {
	if m.sessionID == "" {
		return m, nil
	}

	m.clearTranscript()

	return m, m.send(golemclient.CreateSession{Type: golemclient.TypeCreateSession})
}

// clearTranscript drops every session-scoped piece of local view state:
// blocks and their spans, the model identity, and the status line's
// turn/usage bookkeeping. Session switches (new session, picker load)
// both land here — the incoming session's History/TurnStart repopulates.
func (m *Model) clearTranscript() {
	m.clearSelection()
	m.blocks = nil
	m.userSpans, m.toolSpans = nil, nil
	m.provider, m.modelName = "", ""
	m.endTurn()
	m.lastUsage = golemclient.Usage{}
	m.usageIn, m.usageOut = 0, 0
	m.metaBusy = false
	m.sessionTitle, m.sessionCwd = "", ""
	m.refreshView()
}

// openSessions requests the session list and opens the picker when it
// arrives.
func (m Model) openSessions() (tea.Model, tea.Cmd) {
	m.listRequest = true

	return m, m.send(golemclient.ListSessions{Type: golemclient.TypeListSessions})
}

// openModels shows the model picker with the cursor on the current model.
func (m Model) openModels() (tea.Model, tea.Cmd) {
	m.mode = modeModels
	m.modelCursor = m.cursorOnModel(m.modelName)

	return m, nil
}

// leaderKey dispatches the second key of a ctrl+x sequence. Bound keys
// consume the press; esc cancels; anything else disarms and continues as
// ordinary chat input, so a stray arm never swallows typing (ctrl+x here
// re-arms with a fresh window). Direct chords stay impossible where the
// terminal protocol cannot distinguish them — ctrl+m is enter (CR), which
// is why every command rides the leader namespace.
func (m Model) leaderKey(key tea.KeyMsg) (tea.Model, tea.Cmd) {
	m.setLeader(false)

	if key.Type == tea.KeyEscape {
		return m, nil
	}

	if !key.Alt && key.Type == tea.KeyRunes && len(key.Runes) == 1 {
		switch key.Runes[0] {
		case 'n':
			return m.newSessionCmd()
		case 'l':
			return m.openSessions()
		case 'm':
			return m.openModels()
		case 'q':
			return m.quitCmd()
		}
	}

	return m.updateChat(key)
}

// escKey clears a text selection, then interrupts an active turn; idle
// presses do nothing (quitting is ctrl+c's job).
func (m Model) escKey() (tea.Model, tea.Cmd) {
	if m.selectedText() != "" {
		m.clearSelection()
		m.refreshView()

		return m, nil
	}

	if m.turnActive {
		return m.ctrlC()
	}

	return m, nil
}

// compactCommand is the composer's manual compaction trigger (ADR-0022).
const compactCommand = "/compact"

// compactCmd sends a compact_session event for the current session; with
// no session yet there is nothing to compact.
func (m Model) compactCmd() (tea.Model, tea.Cmd) {
	if m.sessionID == "" || m.client == nil {
		m.blocks = append(m.blocks, block{
			kind: blockError,
			text: "no session to compact yet — send a message first",
		})
		m.refreshView()

		return m, nil
	}

	return m, m.send(golemclient.CompactSession{
		Type: golemclient.TypeCompactSession, Session: m.sessionID,
	})
}

// submit sends the composed line as a chat message. Everything typed is
// content — navigation and lifecycle live on keys, not slash commands.
// With no session yet the line queues and triggers create_session
// (ADR-0018): the session is born with the first word, not the launch.
func (m Model) submit(line string) (tea.Model, tea.Cmd) {
	if line == compactCommand {
		return m.compactCmd()
	}

	if m.client == nil && m.sessionID == "" {
		m.blocks = append(m.blocks, block{kind: blockUser, text: line})
		m.pendingMsgs = append(m.pendingMsgs, line)
		m.follow = true
		m.refreshView()

		return m, nil // pre-connect: connectedMsg kicks off the create
	}

	if m.client == nil {
		m.blocks = append(m.blocks, block{kind: blockError, text: "not connected yet"})
		m.refreshView()

		return m, nil
	}

	if m.sessionID == "" {
		m.blocks = append(m.blocks, block{kind: blockUser, text: line})
		m.pendingMsgs = append(m.pendingMsgs, line)
		m.follow = true
		m.refreshView()

		if len(m.pendingMsgs) == 1 { // first pending line arms the create
			return m, m.send(golemclient.CreateSession{Type: golemclient.TypeCreateSession})
		}

		return m, nil
	}

	m.blocks = append(m.blocks, block{kind: blockUser, text: line})
	m.follow = true
	m.refreshView()

	return m, m.send(golemclient.UserMessage{
		Type: golemclient.TypeUserMessage, Session: m.sessionID, Text: line,
	})
}

// send wraps client.Send so Update can stay a pure-ish value method.
func (m Model) send(event any) tea.Cmd {
	c := m.client

	return func() tea.Msg {
		if c == nil {
			return nil
		}

		if err := c.Send(event); err != nil {
			return connErrMsg{err: err}
		}

		return nil
	}
}
