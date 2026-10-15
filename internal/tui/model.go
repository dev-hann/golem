package tui

import (
	"fmt"
	"path/filepath"
	"strings"
	"time"

	"github.com/charmbracelet/bubbles/textarea"
	"github.com/charmbracelet/bubbles/viewport"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/x/ansi"

	"github.com/dev-hann/golem/internal/tui/kit"
	"github.com/dev-hann/golem/pkg/golemclient"
)

// mode is the TUI's input mode.
type mode int

// Input modes.
const (
	modeChat mode = iota
	modeSessions
	modeModels
	modeCommand
)

// Model is the Bubble Tea model for the golem terminal client.
type Model struct {
	client  client
	dial    dialer
	socket  string
	connErr string
	kit     *kit.Kit

	mode            mode
	sessionID       string
	sessionTitle    string   // current session's title (header); "" while untitled
	sessionCwd      string   // current session's working directory (header tail)
	pendingMsgs     []string // submits awaiting a session (ADR-0018 lazy create)
	pendingModel    string   // set_model deferred until a session exists
	turnActive      bool
	follow          bool
	blocks          []block
	userSpans       []userSpan
	toolSpans       []toolSpan
	leaderArmed     bool
	leaderGen       int
	cmdQuery        string
	cmdCursor       int
	sessions        []golemclient.SessionSummary
	pickIndex       int
	sessQuery       string
	sessSpinFrame   int // picker spinner frame (braille loader for live turns)
	sessTickGen     int // spinner ticker generation (stale-tick guard)
	sessRenaming    bool
	sessRenameText  string
	sessDeleteArmed string // picker row armed for ctrl+d delete (session id)
	sessScroll      int    // picker list's first visible line (group headers included)
	vp              viewport.Model
	input           textarea.Model
	width           int
	height          int
	content         string // plain rendered transcript; the copy source
	selecting       bool   // a left-button drag over the transcript is in progress
	selAnchor       int    // content line where the drag began
	selHead         int    // content line under the drag's pointer
	copier          func(string) error
	provider        string
	modelName       string
	models          []string
	modelCursor     int
	inputRows       int
	quitting        bool
	listRequest     bool
	activeTool      string            // tool name of the in-flight ToolCallStart
	activeArgs      string            // raw args of the in-flight tool call
	lastUsage       golemclient.Usage // usage of the most recent completed turn
	streamedText    bool              // any TextDelta seen this turn (thinking vs responding)
	reasoned        bool              // any ThinkingDelta seen this turn (reasoning state)
	retryState      *retryInfo        // live provider retry, nil when none runs
	synthDone       *toolDone         // last ToolCallEnd while synthesizing follows-up
	metaBusy        bool              // the loaded session's live busy flag (session_meta)
}

// New builds the model for a socket path.
func New(socketPath string) Model {
	return NewThemed(socketPath, kit.New(kit.DefaultTheme()))
}

// NewThemed builds the model with an explicit kit (theme switches
// construct a new model view through this).
func NewThemed(socketPath string, k *kit.Kit) Model {
	input := textarea.New()
	input.Placeholder = chatPlaceholder
	input.ShowLineNumbers = false
	input.FocusedStyle.Text = k.Style(kit.Fg)
	input.FocusedStyle.Placeholder = k.Style(kit.Dim)
	input.BlurredStyle = input.FocusedStyle
	input.SetHeight(minInputHeight)
	input.SetWidth(startWidth - inputBorderCols)
	input.Focus()

	vp := viewport.New(startWidth, startHeight-minInputHeight-inputBorderRows)

	return Model{
		dial:      realDial,
		socket:    socketPath,
		kit:       k,
		vp:        vp,
		input:     input,
		mode:      modeChat,
		inputRows: minInputHeight,
		follow:    true,
	}
}

// Layout constants. The viewport gets whatever height remains. The input
// box grows with its content between minInputHeight and a share of the
// terminal (inputMaxShareNumerator/inputMaxShareDenominator).
const (
	minInputHeight = 1
	// The composer frame adds a rounded border: two rows and two columns.
	inputBorderRows = 2
	inputBorderCols = 2
	// Growing past 40% of the terminal would starve the transcript.
	inputMaxShareNumerator   = 2
	inputMaxShareDenominator = 5
	startWidth               = 80
	startHeight              = 20
)

// Init implements tea.Model: connect and start listening.
func (m Model) Init() tea.Cmd {
	return m.connect()
}

// connect dials the server. Per ADR-0018 no session is created here —
// the first submitted message triggers create_session, so an idle
// launch leaves nothing behind on the server.
func (m Model) connect() tea.Cmd {
	c := m

	return func() tea.Msg {
		conn, err := c.dial(c.socket)
		if err != nil {
			return connErrMsg{err: err}
		}

		return connectedMsg{conn: conn}
	}
}

// listen returns a Cmd reading the next server event.
func (m Model) listen() tea.Cmd {
	c := m.client

	return func() tea.Msg {
		ev, err := c.Receive()
		if err != nil {
			return connErrMsg{err: err}
		}

		return serverEventMsg{ev: ev}
	}
}

// Messages crossing into the Elm loop.
type (
	connectedMsg   struct{ conn client }
	connErrMsg     struct{ err error }
	serverEventMsg struct{ ev any }
)

// leaderTimeoutMsg disarms a leader sequence whose window elapsed; gen
// identifies which arm it belongs to so a stale tick cannot disarm a
// newer sequence.
type leaderTimeoutMsg struct{ gen int }

// sessTickMsg advances the session picker's spinner frame; gen guards
// against a stale tick from a previously opened picker.
type sessTickMsg struct{ gen int }

// sessTickInterval is the session picker's spinner animation rate —
// fast enough to read as motion, slow enough not to churn redraws.
const sessTickInterval = 100 * time.Millisecond

// armSessTick schedules the picker spinner's next frame.
func armSessTick(gen int) tea.Cmd {
	return tea.Tick(sessTickInterval, func(time.Time) tea.Msg {
		return sessTickMsg{gen: gen}
	})
}

// sessTick advances the picker spinner: only an open picker animates —
// a tick from a closed one (or a stale generation) is dropped.
func (m Model) sessTick(msg sessTickMsg) (tea.Model, tea.Cmd) {
	if m.mode == modeSessions && m.sessTickGen == msg.gen {
		m.sessSpinFrame++

		return m, armSessTick(m.sessTickGen)
	}

	return m, nil
}

// leaderTimeout bounds how long a ctrl+x sequence waits for its key.
const leaderTimeout = 2 * time.Second

// armLeader starts the leader timeout window for gen.
func armLeader(gen int) tea.Cmd {
	return tea.Tick(leaderTimeout, func(time.Time) tea.Msg {
		return leaderTimeoutMsg{gen: gen}
	})
}

// Update implements tea.Model. Split by input mode for readability.
func (m Model) Update(msg tea.Msg) (tea.Model, tea.Cmd) { //nolint:revive // interface contract
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.resize(msg.Width, msg.Height)
		return m, nil
	case connectedMsg:
		m.client = msg.conn
		// A pre-connect submit armed the lazy create (ADR-0018).
		if len(m.pendingMsgs) > 0 {
			return m, tea.Batch(m.listen(), m.send(golemclient.CreateSession{Type: golemclient.TypeCreateSession}))
		}

		return m, m.listen()
	case connErrMsg:
		if m.quitting {
			return m, tea.Quit
		}

		m.connErr = msg.err.Error()

		return m, tea.Quit
	case serverEventMsg:
		cmd := m.applyEvent(msg.ev)
		return m, cmd
	case leaderTimeoutMsg:
		if m.leaderArmed && m.leaderGen == msg.gen {
			m.setLeader(false)
		}

		return m, nil
	case sessTickMsg:
		return m.sessTick(msg)
	case tea.MouseMsg:
		m.handleMouse(msg)

		return m, nil
	case copyErrMsg:
		m.blocks = append(m.blocks, block{kind: blockError, text: "copy failed: " + msg.err.Error()})
		m.refreshView()

		return m, nil
	case tea.KeyMsg:
		return m.handleKey(msg)
	default:
		return m, nil
	}
}

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

// ctrlC mirrors opencode's exit ladder: an active turn interrupts, then
// the composer clears, and only an empty composer quits.
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

	return m.quitCmd()
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
// hint points at the key-list reference while a ctrl+x sequence pends.
const (
	chatPlaceholder   = "message… (enter send · alt+enter newline · ctrl+p commands)"
	leaderPlaceholder = "shortcut mode — n new · l sessions · m model · q quit · esc cancels"
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

// command is one entry of the ctrl+p command palette; run executes the
// action on the model.
type command struct {
	name string
	keys string
	run  func(Model) (tea.Model, tea.Cmd)
}

// commands lists the palette entries in display order — every leader
// action plus quit, mirroring the opencode command list.
func (m Model) commands() []command {
	return []command{
		{name: "new session", keys: "ctrl+x n", run: Model.newSessionCmd},
		{name: "session list", keys: "ctrl+x l", run: Model.openSessions},
		{name: "model list", keys: "ctrl+x m", run: Model.openModels},
		{name: "compact session", keys: "/compact", run: Model.paletteCompact},
		{name: "quit", keys: "ctrl+d", run: Model.quitCmd},
	}
}

// paletteCompact runs the compaction from the command palette.
func (m Model) paletteCompact() (tea.Model, tea.Cmd) {
	return m.compactCmd()
}

// filteredCommands returns the palette commands whose names match the
// query.
func (m Model) filteredCommands() []command {
	if m.cmdQuery == "" {
		return m.commands()
	}

	var out []command

	for _, c := range m.commands() {
		if fuzzyContains(m.cmdQuery, c.name) {
			out = append(out, c)
		}
	}

	return out
}

// fuzzyContains reports whether the query's runes appear in s in order
// (case-insensitive subsequence match — "ns" hits "new session").
func fuzzyContains(query, s string) bool {
	q := []rune(strings.ToLower(query))
	i := 0

	for _, r := range strings.ToLower(s) {
		if i < len(q) && r == q[i] {
			i++
		}
	}

	return i == len(q)
}

// updateCommand drives the ctrl+p palette: typing filters, arrows (or
// the dialog keys ctrl+p/ctrl+n) move, enter runs, esc closes.
func (m Model) updateCommand(key tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch key.Type { //nolint:exhaustive // foreign enum; default ignores
	case tea.KeyEscape:
		m.mode = modeChat
		m.cmdQuery, m.cmdCursor = "", 0

		return m, nil
	case tea.KeyUp, tea.KeyCtrlP:
		m.cmdCursor = max(m.cmdCursor-1, 0)

		return m, nil
	case tea.KeyDown, tea.KeyCtrlN:
		m.cmdCursor = min(m.cmdCursor+1, len(m.filteredCommands())-1)

		return m, nil
	case tea.KeyEnter:
		cmds := m.filteredCommands()
		if len(cmds) == 0 {
			return m, nil
		}

		chosen := cmds[max(min(m.cmdCursor, len(cmds)-1), 0)]
		m.mode = modeChat
		m.cmdQuery, m.cmdCursor = "", 0

		return chosen.run(m)
	case tea.KeyBackspace:
		if runes := []rune(m.cmdQuery); len(runes) > 0 {
			m.cmdQuery = string(runes[:len(runes)-1])
			m.cmdCursor = max(min(m.cmdCursor, len(m.filteredCommands())-1), 0)
		}

		return m, nil
	case tea.KeyRunes:
		if !key.Alt {
			m.cmdQuery += string(key.Runes)
			m.cmdCursor = max(min(m.cmdCursor, len(m.filteredCommands())-1), 0)
		}

		return m, nil
	default:
		return m, nil
	}
}

// commandBody renders the palette: matching rows with their shortcuts,
// the live query, and the non-action key hints the help modal used to
// carry.
func (m Model) commandBody() string {
	var b strings.Builder

	cmds := m.filteredCommands()
	if len(cmds) == 0 {
		b.WriteString(m.kit.DimLine("no matches") + "\n")
	}

	for i, c := range cmds {
		row := fmt.Sprintf("%-13s %s", c.name, c.keys)
		fmt.Fprintf(&b, "%s\n", m.kit.Row(row, i == m.cmdCursor)) //nolint:errcheck // strings.Builder
	}

	b.WriteString("\n" + m.kit.Prompt("› "+m.cmdQuery+"_") + "\n")
	b.WriteString(m.kit.DimLine("type to filter · ↑↓ select · enter run · esc close") + "\n")
	b.WriteString(m.kit.DimLine("enter send · alt+enter/ctrl+j newline · ctrl+g top · ctrl+alt+g bottom"))

	return b.String()
}

// pageLines is the scroll step for page keys.
const pageLines = 10

// scrollBy scrolls the transcript by delta lines (negative scrolls back
// toward older output). Manual scrollback suspends auto-follow; reaching
// the bottom resumes it.
func (m *Model) scrollBy(delta int) {
	if delta < 0 {
		m.follow = false
		m.vp.ScrollUp(-delta)

		return
	}

	m.vp.ScrollDown(delta)

	if m.vp.AtBottom() {
		m.follow = true
	}
}

// wheelLines is the scroll step for one mouse-wheel notch.
const wheelLines = 3

// scrollKey handles transcript-scroll keys, reporting whether it
// consumed the key. ctrl+g / ctrl+alt+g jump to the top / bottom; plain
// home / end stay with the composer, matching opencode's split.
func (m *Model) scrollKey(key tea.KeyMsg) bool {
	switch key.Type { //nolint:exhaustive // only the scroll keys are ours
	case tea.KeyPgUp:
		m.scrollBy(-pageLines)

		return true
	case tea.KeyPgDown:
		m.scrollBy(pageLines)

		return true
	case tea.KeyCtrlG:
		if key.Alt {
			m.vp.GotoBottom()
			m.follow = true
		} else {
			m.vp.GotoTop()
			m.follow = false
		}

		return true
	default:
		return false
	}
}

// mouseClick toggles the tool block under a left click — the chevron
// rows — reporting whether one was hit. Clicks on the composer, the
// sticky header, or modal backdrops are not tool rows and do nothing.
func (m *Model) mouseClick(msg tea.MouseMsg) bool {
	if m.mode != modeChat {
		return false
	}

	line, ok := m.lineAt(msg.Y)
	if !ok {
		return false
	}

	for _, span := range m.toolSpans {
		if line >= span.start && line < span.end {
			m.blocks[span.index].expanded = !m.blocks[span.index].expanded
			m.refreshView()

			return true
		}
	}

	return false
}

// handleMouse routes mouse input: the wheel scrolls whatever modal is
// open (the session picker's list) or the transcript, a left press
// toggles the tool row under it or begins a text selection, drags
// extend the selection, and the release finishes it.
func (m *Model) handleMouse(msg tea.MouseMsg) {
	switch {
	case msg.Button == tea.MouseButtonWheelUp:
		if m.mode == modeSessions {
			m.scrollPicker(-wheelLines)

			return
		}

		m.scrollBy(-wheelLines)
	case msg.Button == tea.MouseButtonWheelDown:
		if m.mode == modeSessions {
			m.scrollPicker(wheelLines)

			return
		}

		m.scrollBy(wheelLines)
	case msg.Button == tea.MouseButtonLeft && msg.Action == tea.MouseActionPress:
		if !m.mouseClick(msg) {
			m.beginSelection(msg.Y)
		}
	case msg.Button == tea.MouseButtonLeft && msg.Action == tea.MouseActionMotion:
		m.extendSelection(msg.Y)
	case msg.Action == tea.MouseActionRelease &&
		(msg.Button == tea.MouseButtonLeft || msg.Button == tea.MouseButtonNone):
		m.endSelection()
	default:
	}
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

// updateModels drives the model picker modal: arrows move, enter applies
// via set_model, esc cancels.
func (m Model) updateModels(key tea.KeyMsg) (tea.Model, tea.Cmd) {
	if len(m.models) == 0 {
		m.mode = modeChat

		return m, nil
	}

	switch key.Type { //nolint:exhaustive // foreign enum; default ignores
	case tea.KeyUp, tea.KeyCtrlP:
		m.modelCursor = max(m.modelCursor-1, 0)
	case tea.KeyDown, tea.KeyCtrlN:
		m.modelCursor = min(m.modelCursor+1, len(m.models)-1)
	case tea.KeyEsc:
		m.mode = modeChat
	case tea.KeyEnter:
		chosen := m.models[m.modelCursor]
		m.modelName = chosen
		m.mode = modeChat

		// No session yet (ADR-0018): remember the choice and apply it
		// when session_meta arrives.
		if m.sessionID == "" {
			m.pendingModel = chosen
			return m, nil
		}

		return m, m.send(golemclient.SetModel{
			Type: golemclient.TypeSetModel, Session: m.sessionID, Model: chosen,
		})
	default:
	}

	return m, nil
}

// cursorOnModel prepositions the picker cursor on the current model.
func (m Model) cursorOnModel(current string) int {
	for i, model := range m.models {
		if model == current {
			return i
		}
	}

	return 0
}

// WithModels supplies the provider's model list for the ctrl+m picker.
func (m Model) WithModels(models []string) Model {
	m.models = models

	return m
}

// updateSessions drives the session picker: typing filters (fuzzy over
// title and directory), ctrl+r renames the highlighted row inline,
// ctrl+d deletes it (press twice to confirm — opencode's pattern), and
// the dialog keys move/confirm. esc clears the query first, then closes.
// Every cursor or filter change funnels through one trailing
// scrollToCursor so the window tracks the cursor.
func (m Model) updateSessions(key tea.KeyMsg) (tea.Model, tea.Cmd) {
	if m.sessRenaming {
		return m.renameKey(key)
	}

	filtered := m.filteredSessions()

	switch key.Type { //nolint:exhaustive // foreign enum; default filters
	case tea.KeyEsc:
		m.sessDeleteArmed = ""

		if m.sessQuery != "" {
			m.sessQuery = ""
			m.pickIndex = 0

			return m, nil
		}

		m.mode = modeChat

		return m, nil
	case tea.KeyCtrlR:
		if len(filtered) == 0 {
			return m, nil
		}

		m.sessRenaming = true
		m.sessRenameText = filtered[m.pickIndex].Title

		return m, nil
	case tea.KeyCtrlD:
		return m.deleteKey(filtered)
	case tea.KeyUp, tea.KeyCtrlP:
		m.sessDeleteArmed = ""
		m.pickIndex = max(m.pickIndex-1, 0)
	case tea.KeyDown, tea.KeyCtrlN:
		m.sessDeleteArmed = ""
		m.pickIndex = min(m.pickIndex+1, len(filtered)-1)
	case tea.KeyBackspace:
		m.sessDeleteArmed = ""

		if runes := []rune(m.sessQuery); len(runes) > 0 {
			m.sessQuery = string(runes[:len(runes)-1])
			m.pickIndex = min(m.pickIndex, max(len(m.filteredSessions())-1, 0))
		}
	case tea.KeyEnter:
		if len(filtered) == 0 {
			return m, nil
		}

		chosen := filtered[m.pickIndex]
		m.mode = modeChat
		m.sessDeleteArmed = ""
		m.clearTranscript()

		return m, m.send(golemclient.LoadSession{
			Type: golemclient.TypeLoadSession, Session: chosen.ID,
		})
	case tea.KeyRunes:
		if !key.Alt {
			m.sessDeleteArmed = ""
			m.sessQuery += string(key.Runes)
			m.pickIndex = min(m.pickIndex, max(len(m.filteredSessions())-1, 0))
		}
	default:
		return m, nil
	}

	m.scrollToCursor()

	return m, nil
}

// removeSession drops a session from the picker list, clamping the
// cursor and scroll back into range.
func (m *Model) removeSession(id string) {
	out := m.sessions[:0:0]
	for _, s := range m.sessions {
		if s.ID != id {
			out = append(out, s)
		}
	}

	m.sessions = out
	m.pickIndex = min(m.pickIndex, max(len(m.filteredSessions())-1, 0))
	m.scrollToCursor()
}

// deleteKey handles the picker's ctrl+d: the first press arms the
// highlighted row, the second deletes it optimistically (opencode
// removes the row immediately) and lets session_deleted reconcile.
func (m Model) deleteKey(filtered []golemclient.SessionSummary) (tea.Model, tea.Cmd) {
	if len(filtered) == 0 {
		return m, nil
	}

	chosen := filtered[m.pickIndex]

	if m.sessDeleteArmed != chosen.ID {
		m.sessDeleteArmed = chosen.ID

		return m, nil
	}

	m.sessDeleteArmed = ""
	m.removeSession(chosen.ID)

	if m.sessionID == chosen.ID { // deleted the session we stand in
		m.clearTranscript()
		m.sessionID = ""
	}

	return m, m.send(golemclient.DeleteSession{
		Type: golemclient.TypeDeleteSession, Session: chosen.ID,
	})
}

// renameKey edits the highlighted session's title: typing and backspace
// edit, ctrl+u clears, enter commits (optimistically — the server's
// session_title reply reconciles), esc cancels.
func (m Model) renameKey(key tea.KeyMsg) (tea.Model, tea.Cmd) {
	filtered := m.filteredSessions()

	commit := func() (tea.Model, tea.Cmd) {
		chosen := filtered[m.pickIndex]

		title := strings.TrimSpace(m.sessRenameText)
		if title == "" {
			m.sessRenaming = false
			m.sessRenameText = ""

			return m, nil
		}

		m.sessRenaming = false
		m.sessRenameText = ""

		if i := chosenIndex(m.sessions, chosen.ID); i >= 0 {
			m.sessions[i].Title = title
		}

		if chosen.ID == m.sessionID {
			m.sessionTitle = title
		}

		return m, m.send(golemclient.RenameSession{
			Type: golemclient.TypeRenameSession, Session: chosen.ID, Title: title,
		})
	}

	switch key.Type { //nolint:exhaustive // foreign enum; default ignores
	case tea.KeyEscape:
		m.sessRenaming = false
		m.sessRenameText = ""

		return m, nil
	case tea.KeyEnter:
		return commit()
	case tea.KeyCtrlU:
		m.sessRenameText = ""

		return m, nil
	case tea.KeyBackspace:
		if runes := []rune(m.sessRenameText); len(runes) > 0 {
			m.sessRenameText = string(runes[:len(runes)-1])
		}

		return m, nil
	case tea.KeyRunes:
		if !key.Alt {
			m.sessRenameText += string(key.Runes)
		}

		return m, nil
	default:
		return m, nil
	}
}

// filteredSessions returns the rows matching the picker's search query.
func (m Model) filteredSessions() []golemclient.SessionSummary {
	if m.sessQuery == "" {
		return m.sessions
	}

	out := make([]golemclient.SessionSummary, 0, len(m.sessions))

	for _, s := range m.sessions {
		if fuzzyContains(m.sessQuery, s.Title+" "+dirName(s.Cwd)) {
			out = append(out, s)
		}
	}

	return out
}

// chosenIndex locates a session id in the full list; -1 when absent.
func chosenIndex(sessions []golemclient.SessionSummary, id string) int {
	for i := range sessions {
		if sessions[i].ID == id {
			return i
		}
	}

	return -1
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

// resize recomputes layout on terminal size changes. The selection
// drops too: rewrapping moves the content lines its indices point at.
func (m *Model) resize(width, height int) {
	m.clearSelection()
	m.width, m.height = width, height
	m.vp.Width = max(width-2*screenPadH, viewportMinWidth)
	setViewportHeightKeepingFollow(&m.vp, max(
		height-m.inputRows-inputBorderRows-statusRows-layoutChromeRows-headerRowsIfShown(height), viewportMinHeight,
	))
	m.input.SetWidth(max(width-2*screenPadH-inputBorderCols, viewportMinWidth))
	m.syncInputHeight()
}

// syncInputHeight grows the input box with its content — one row per
// visual line the textarea actually renders (see composerRows), min
// minInputHeight, capped at the terminal share — and returns the freed or
// claimed rows to the transcript viewport.
func (m *Model) syncInputHeight() {
	rows := composerRows(m.input.Value(), m.input.Width())

	height := m.height
	if height == 0 {
		height = startHeight
	}

	rowCap := max(height*inputMaxShareNumerator/inputMaxShareDenominator, minInputHeight)
	rows = min(max(rows, minInputHeight), rowCap)

	if rows == m.inputRows {
		return
	}

	m.inputRows = rows
	m.input.SetHeight(rows)

	if m.height > 0 {
		setViewportHeightKeepingFollow(&m.vp, max(
			m.height-rows-inputBorderRows-statusRows-layoutChromeRows-headerRowsIfShown(m.height), viewportMinHeight,
		))
	}
}

// setViewportHeightKeepingFollow resizes the viewport and, while the
// reader sits at the tail (or above any content), re-pins it. Without
// the re-pin, shrinking the terminal (or the composer growing) strands
// the last line below the fold — the viewport keeps its old offset over
// a shorter window. A scrolled-back reader keeps their offset: follow
// stays suspended until they page to the bottom again.
func setViewportHeightKeepingFollow(vp *viewport.Model, height int) {
	// AtBottom must be judged against the old height: after shrinking,
	// the stale offset is past the new fold and no longer "at bottom".
	atTail := vp.AtBottom() || vp.YOffset == 0

	vp.Height = height

	if atTail {
		vp.GotoBottom()
	}
}

// Screen padding: the chat screen sits inside a margin instead of
// touching the terminal edges. screenPadH indents both sides, screenPadV
// blanks rows above and below, and composerGapRows separates the
// transcript from the composer. layoutChromeRows totals the rows the
// padding claims so the viewport can shrink by exactly that much — the
// screen must still fill the terminal height exactly (jitter guard).
const (
	screenPadH = 2
	screenPadV = 1

	composerGapRows = 1

	layoutChromeRows = 2*screenPadV + composerGapRows
)

// Layout minimums so tiny terminals still render.
const (
	viewportMinWidth  = 20
	viewportMinHeight = 3
)

// statusRows is the height the status line borrows from the viewport —
// always rendered, so the layout never jumps between idle and active.
const statusRows = 1

// headerRows is the height the session header borrows from the viewport
// — always rendered on roomy terminals (same anti-jump rule as the
// status line), dropped entirely on terminals too short to afford it so
// the viewport keeps its floor.
const headerRows = 1

// headerMinHeight is the smallest terminal that still shows the header;
// below it the transcript's floor claims the row.
const headerMinHeight = 16

// applyEvent folds one server event into the model, returning the next cmd.
// sessionOf extracts the session a streaming event belongs to; "" for
// events that are session-less or carry a new session identity (SessionMeta).
func sessionOf(ev any) string {
	switch e := ev.(type) {
	case golemclient.TurnStart:
		return e.Session
	case golemclient.TextDelta:
		return e.Session
	case golemclient.ThinkingDelta:
		return e.Session
	case golemclient.Retry:
		return e.Session
	case golemclient.ToolCallStart:
		return e.Session
	case golemclient.ToolCallEnd:
		return e.Session
	case golemclient.ErrorEvent:
		return e.Session
	case golemclient.TurnEnd:
		return e.Session
	case golemclient.History:
		return e.Session
	default:
		return ""
	}
}

// skipStaleEvent drops streaming from a session this client no longer shows
// (a fresh ctrl+n session started while a turn was in flight), reporting
// whether it swallowed the event along with the keep-listening cmd.
func (m *Model) skipStaleEvent(ev any) (tea.Cmd, bool) {
	if m.sessionID == "" {
		return nil, false
	}

	s := sessionOf(ev)
	if s == "" || s == m.sessionID {
		return nil, false
	}

	if m.client != nil {
		return m.listen(), true
	}

	return nil, true
}

func (m *Model) applyEvent(ev any) tea.Cmd {
	if cmd, stale := m.skipStaleEvent(ev); stale {
		return cmd
	}

	var (
		tick tea.Cmd
		cmd  tea.Cmd
	)

	switch e := ev.(type) {
	case golemclient.SessionMeta:
		m.sessionID = e.Session
		m.sessionTitle, m.sessionCwd = e.Title, e.Cwd
		m.metaBusy = e.Busy
		cmd = m.flushPending()
	case golemclient.SessionList:
		tick = m.applySessionList(e)
	case golemclient.History:
		m.replayHistory(e)
	default:
		// Turn events fold through applyTurnEvent; session records
		// (title, deletion, compaction) through applySessionRecord;
		// anything else is ignored per PROTOCOL.md forward
		// compatibility.
		m.applyTurnEvent(ev)
		m.applySessionRecord(ev)
	}

	m.refreshView()

	// tea.Batch skips nil cmds, so a pre-connect event or a non-lifecycle
	// event needs no nil plumbing here.
	return tea.Batch(tick, cmd, m.listenCmd())
}

// flushPending sends the messages (and a deferred set_model) that were
// queued before the session existed. The sends run inline rather than as
// cmds: wire order matters (set_model applies "from the next turn", so it
// must land before the first user_message) and tea.Batch executes
// concurrently. A send failure surfaces as connErrMsg.
func (m *Model) flushPending() tea.Cmd {
	if m.client == nil || (len(m.pendingMsgs) == 0 && m.pendingModel == "") {
		return nil
	}

	var sendErr error

	if m.pendingModel != "" {
		sendErr = m.client.Send(golemclient.SetModel{
			Type: golemclient.TypeSetModel, Session: m.sessionID, Model: m.pendingModel,
		})
		m.pendingModel = ""
	}

	for _, line := range m.pendingMsgs {
		if sendErr == nil {
			sendErr = m.client.Send(golemclient.UserMessage{
				Type: golemclient.TypeUserMessage, Session: m.sessionID, Text: line,
			})
		}
	}

	m.pendingMsgs = nil

	if sendErr != nil {
		return func() tea.Msg { return connErrMsg{err: sendErr} }
	}

	return nil
}

// listenCmd returns the keep-listening command when a connection exists.
func (m *Model) listenCmd() tea.Cmd {
	if m.client == nil {
		return nil
	}

	return m.listen()
}

// endTurn clears the active-turn bookkeeping shared by turn_end and
// error events.
func (m *Model) endTurn() {
	m.turnActive = false
	m.activeTool, m.activeArgs = "", ""
	m.streamedText = false
	m.reasoned = false
	m.retryState = nil
	m.synthDone = nil
}

// applySessionList stores a session list and, when it was explicitly
// requested, opens the picker on it with fresh search state, the cursor
// starting on the session this client is in. Opening arms the spinner
// ticker; closing the picker orphans its generation (stale ticks drop).
func (m *Model) applySessionList(e golemclient.SessionList) tea.Cmd {
	m.sessions = e.Sessions

	m.sessQuery = ""
	m.sessRenaming = false
	m.sessRenameText = ""
	m.sessDeleteArmed = ""
	m.sessScroll = 0
	m.pickIndex = m.cursorOnSession()

	if m.listRequest {
		m.listRequest = false
		m.mode = modeSessions

		if anyRunning(m) {
			m.sessSpinFrame = 0
			m.sessTickGen++

			return armSessTick(m.sessTickGen)
		}
	}

	return nil
}

// applyTurnEvent folds one turn-lifecycle event — streaming text,
// reasoning, retries, tool calls, and the turn's terminal events. It is
// a no-op for anything that is not a turn event.
func (m *Model) applyTurnEvent(ev any) {
	switch e := ev.(type) {
	case golemclient.TurnStart:
		m.turnActive = true
		m.follow = true
		m.provider = e.Provider
		m.modelName = e.Model
		m.streamedText = false
		m.reasoned = false
		m.retryState = nil
		m.activeTool, m.activeArgs = "", ""
		m.synthDone = nil
	case golemclient.TextDelta:
		m.appendToAssistant(e.Delta)
		m.streamedText = true
		m.retryState = nil // a retry only precedes first content
		m.synthDone = nil  // the model's follow-up has begun
	case golemclient.ThinkingDelta:
		m.reasoned = true
		m.retryState = nil
	case golemclient.Retry:
		m.retryState = &retryInfo{Attempt: e.Attempt, Retries: e.Retries, DelayMs: e.DelayMs}
	case golemclient.ToolCallStart:
		m.blocks = append(m.blocks, block{
			kind: blockTool, callID: e.CallID, tool: e.Tool, args: string(e.Args),
		})
		m.activeTool, m.activeArgs = e.Tool, string(e.Args)
		m.synthDone = nil
	case golemclient.ToolCallEnd:
		m.finishToolBlock(e)
		m.synthDone = &toolDone{Tool: m.activeTool, OK: e.OK}
		m.activeTool, m.activeArgs = "", ""
		m.retryState = nil
	case golemclient.ErrorEvent:
		m.blocks = append(m.blocks, block{kind: blockError, text: e.Message})
		m.endTurn()
	case golemclient.TurnEnd:
		m.lastUsage = e.Usage
		m.endTurn()
	default:
		// not a turn event; nothing to fold
	}
}

// applySessionRecord folds the non-streaming session record events —
// title, deletion, compaction, and compaction failure — that carry no
// turn lifecycle.
func (m *Model) applySessionRecord(ev any) {
	switch e := ev.(type) {
	case golemclient.SessionTitle:
		if i := chosenIndex(m.sessions, e.Session); i >= 0 {
			m.sessions[i].Title = e.Title
		}

		if e.Session == m.sessionID {
			m.sessionTitle = e.Title
		}
	case golemclient.SessionDeleted:
		m.applySessionDeleted(e)
	case golemclient.SessionCompacted:
		m.applyCompacted(e)
	case golemclient.CompactFailed:
		m.applyCompactFailed(e)
	default:
		// not a session record; nothing to fold
	}
}

// applyCompacted folds the compaction barrier confirmation: a dim
// summary block lands in the transcript.
func (m *Model) applyCompacted(e golemclient.SessionCompacted) {
	m.blocks = append(m.blocks, blockDimCompacted(e))
}

// applyCompactFailed folds a failed compaction: an error block carrying
// the reason; the transcript is otherwise untouched.
func (m *Model) applyCompactFailed(e golemclient.CompactFailed) {
	m.blocks = append(m.blocks, block{
		kind: blockError, text: "compact failed: " + e.Reason,
	})
}

// applySessionDeleted folds the server's delete confirmation: drop the
// row (already gone for the optimistic deleter — a no-op), and if the
// deleted session is the one this client stands in, fall back to the
// lazy-start state (no session until the next message, ADR-0018).
func (m *Model) applySessionDeleted(e golemclient.SessionDeleted) {
	m.removeSession(e.Session)
	m.sessDeleteArmed = ""

	if m.sessionID == e.Session {
		m.clearTranscript()
		m.sessionID = ""
	}
}

// anyRunning reports whether any listed session has a live turn — the
// cue to spend ticks on the picker's spinner.
func anyRunning(m *Model) bool {
	for _, s := range m.sessions {
		if m.sessionRunning(s) {
			return true
		}
	}

	return false
}

// cursorOnSession prepositions the picker cursor on the current session;
// 0 when it is absent from the list.
func (m *Model) cursorOnSession() int {
	for i := range m.sessions {
		if m.sessions[i].ID == m.sessionID {
			return i
		}
	}

	return 0
}

// appendToAssistant streams deltas into the trailing assistant block.
func (m *Model) appendToAssistant(delta string) {
	if len(m.blocks) == 0 || m.blocks[len(m.blocks)-1].kind != blockAssistant {
		m.blocks = append(m.blocks, block{kind: blockAssistant})
	}

	last := &m.blocks[len(m.blocks)-1]
	last.text += delta
}

// blockDimCompacted builds the dim block confirming a compaction.
func blockDimCompacted(e golemclient.SessionCompacted) block {
	return block{
		kind: blockDim,
		text: "∴ context compacted — earlier history summarized:\n" + e.Summary,
	}
}

// finishToolBlock marks the matching tool block done with its outcome.
func (m *Model) finishToolBlock(end golemclient.ToolCallEnd) {
	for i := len(m.blocks) - 1; i >= 0; i-- {
		if m.blocks[i].kind == blockTool && m.blocks[i].callID == end.CallID {
			m.blocks[i].done = true
			m.blocks[i].ok = end.OK
			m.blocks[i].result = end.Result

			return
		}
	}
}

// replayTurn tracks the in-flight turn while folding a history batch:
// whether the replay ends mid-turn, and what the status line needs to
// render it (any streamed text, the active tool call).
type replayTurn struct {
	open         bool
	streamedText bool
	reasoned     bool
	tool, args   string
	done         *toolDone
	retry        *retryInfo
}

// replayHistory rebuilds the transcript from a history event batch.
// Resuming a session opens at its end — follow is restored so the tail
// (where the conversation continues) is on screen, matching what a live
// stream would leave behind.
//
// Turn lifecycle events fold into status bookkeeping instead of blocks:
// the last turn_start names the model, the last turn_end carries usage.
// When the meta announced a live turn (busy) and the replay ends
// mid-turn — persist-before-emit guarantees the in-flight events are
// already stored — the turn resumes as active, so a client switching
// away and back renders the running state instead of "idle".
func (m *Model) replayHistory(hist golemclient.History) {
	m.blocks = nil
	m.follow = true

	live := replayTurn{}

	for _, raw := range hist.Events {
		ev, err := golemclient.Decode(raw)
		if err != nil {
			continue
		}

		live = m.foldReplayEvent(ev, live)
	}

	m.refreshView()
	m.vp.GotoBottom()

	busy := m.metaBusy
	m.metaBusy = false // the flag scopes to this one replay

	if !busy || !live.open {
		return
	}

	m.turnActive = true
	m.streamedText = live.streamedText
	m.reasoned = live.reasoned
	m.activeTool, m.activeArgs = live.tool, live.args
	m.synthDone = live.done
	m.retryState = live.retry
}

// foldReplayEvent folds one replayed event into transcript blocks,
// threading the live-turn state, and returns the updated state. A
// compaction barrier (ADR-0022) drops every block before it — the same
// fold rule the server applies to context.
func (m *Model) foldReplayEvent(ev any, live replayTurn) replayTurn {
	switch e := ev.(type) {
	case golemclient.SessionCompacted:
		m.blocks = []block{blockDimCompacted(e)}
		return replayTurn{}
	case golemclient.UserMessage:
		m.blocks = append(m.blocks, block{kind: blockUser, text: e.Text})
		return replayTurn{open: true} // a message opens its turn
	case golemclient.TurnStart:
		m.provider, m.modelName = e.Provider, e.Model
		return replayTurn{open: true}
	case golemclient.TextDelta:
		m.appendToAssistant(e.Delta)

		live.streamedText = true

		return live
	case golemclient.ThinkingDelta:
		live.reasoned = true

		return live
	case golemclient.Retry:
		live.retry = &retryInfo{Attempt: e.Attempt, Retries: e.Retries, DelayMs: e.DelayMs}

		return live
	case golemclient.ToolCallStart:
		m.blocks = append(m.blocks, block{
			kind: blockTool, callID: e.CallID, tool: e.Tool, args: string(e.Args),
		})

		live.tool, live.args = e.Tool, string(e.Args)
		live.done = nil

		return live
	case golemclient.ToolCallEnd:
		m.finishToolBlock(e)

		live.done = &toolDone{Tool: live.tool, OK: e.OK}
		live.tool, live.args = "", ""

		return live
	case golemclient.ErrorEvent:
		m.blocks = append(m.blocks, block{kind: blockError, text: e.Message})
		return replayTurn{} // errors end the turn
	case golemclient.TurnEnd:
		m.lastUsage = e.Usage
		return replayTurn{}
	default:
		// Unknown types are ignored per PROTOCOL.md forward compatibility.
		return live
	}
}

// refreshView pushes the rendered transcript into the viewport. While a
// turn streams, the viewport pins to the bottom unless the user has
// paged back (follow=false until they reach the bottom again or send a
// new message).
func (m *Model) refreshView() {
	content, spans, toolSpans := renderBlocks(m.kit, m.blocks, m.vp.Width)
	m.userSpans = spans
	m.toolSpans = toolSpans
	m.content = content
	m.vp.SetContent(m.highlight(content))

	if m.turnActive && m.follow {
		m.vp.GotoBottom()
	}
}

// ConnErr reports the connection error that ended the session, if any.
func (m Model) ConnErr() string {
	return m.connErr
}

// View implements tea.Model.
func (m Model) View() string {
	if m.connErr != "" {
		return "golem: " + m.connErr + "\n"
	}

	switch m.mode {
	case modeCommand:
		return m.modalized(m.kit.Modal(m.vp.Width, "commands", m.commandBody()))
	case modeModels:
		return m.modalized(m.kit.Modal(m.vp.Width, "models · "+m.provider, m.modelsBody()))
	case modeSessions:
		return m.modalized(m.kit.Modal(m.vp.Width, "sessions", m.sessionsBody()))
	case modeChat:
		// The full layout below.
	default:
		// Future modes fall through to the layout.
	}

	return m.layout()
}

// layout composes the full chat screen through the active kit.
func (m Model) layout() string {
	var b strings.Builder
	if m.headerShown() {
		b.WriteString(m.headerLine() + "\n")
	}

	if text := m.stickyText(); text != "" {
		b.WriteString(m.kit.StickyUser(m.vp.Width, text) + "\n")
		b.WriteString(m.shrunkViewport() + "\n")
	} else {
		b.WriteString(m.vp.View() + "\n")
	}

	b.WriteString(m.statusLine() + "\n")

	edge := kit.Border
	if m.leaderArmed {
		edge = kit.Accent
	}

	b.WriteString("\n") // the gap between transcript and composer
	b.WriteString(m.kit.Box(m.input.View(), edge))

	return padScreen(b.String())
}

// padScreen frames the composed screen with its padding: every row gets
// the left pad, blank rows sit above and below. The caller already
// shrunk the viewport by layoutChromeRows so the padded result still
// fills the terminal height exactly.
func padScreen(body string) string {
	pad := strings.Repeat(" ", screenPadH)

	rows := strings.Split(body, "\n")
	for i := range rows {
		rows[i] = pad + rows[i]
	}

	blank := make([]string, screenPadV)
	for i := range blank {
		blank[i] = pad
	}

	out := make([]string, 0, len(rows)+2*screenPadV)
	out = append(out, blank...)
	out = append(out, rows...)
	out = append(out, blank...)

	return strings.Join(out, "\n")
}

// headerShown reports whether the session header renders on this
// terminal — roomy enough to afford the row, else the transcript keeps
// it. The screenPadV offset shifts by the same amount, so the padding
// geometry stays in sync with the resize chrome arithmetic.
func (m Model) headerShown() bool {
	return m.height >= headerMinHeight
}

// headerRowsIfShown is the header's chrome contribution at resize time.
func headerRowsIfShown(height int) int {
	if height >= headerMinHeight {
		return headerRows
	}

	return 0
}

// headerLine renders the one-row session header: the title (or a dim
// placeholder naming the state) with the working-directory tail —
// always exactly one row, truncated to the viewport width.
func (m Model) headerLine() string {
	title, token := m.headerTitle()

	left := title
	if dir := m.headerDir(); dir != "" {
		left += headerDirSep + dir
	}

	return m.kit.StatusBar(m.vp.Width, left, token)
}

// headerTitle resolves the header's title text and token: a titled
// session names itself, an untitled one and the pre-session state fall
// back to dim placeholders.
func (m Model) headerTitle() (string, kit.Token) {
	switch {
	case m.sessionID == "":
		return "no session yet", kit.Dim
	case m.sessionTitle != "":
		return m.sessionTitle, kit.Title
	default:
		return "untitled session", kit.Dim
	}
}

// headerDir renders the working-directory tail beside the title: the
// final path segment (empty until a session exists).
func (m Model) headerDir() string {
	return dirName(m.sessionCwd)
}

// headerDirSep joins the header's title and directory segments.
const headerDirSep = " · "

// statusLine renders the agent-state strip: what the turn is doing (or
// the model and last usage while idle). The interrupt hint lives on the
// key itself, not in the strip.
func (m Model) statusLine() string {
	left, token := m.activeState()
	return m.kit.StatusBar(m.vp.Width, left, token)
}

// activeState resolves the status strip's label and token color from the
// turn state. States, in priority order: a live retry outranks
// everything (its stall is the thing needing explanation), then the
// in-flight tool, then synthesizing (results back at the model), then
// reasoning, responding, thinking. Reasoned-then-text shows responding —
// the answer has begun.
func (m Model) activeState() (string, kit.Token) {
	if m.turnActive {
		return m.activeTurnState()
	}

	left, token := "● idle", kit.Dim

	switch {
	case m.sessionID == "" && (len(m.pendingMsgs) > 0 || m.pendingModel != ""):
		left = "● creating session"
		token = kit.Accent
	case m.modelName != "":
		left = "● " + m.modelName + usageSummary(m.lastUsage)
	}

	return left, token
}

// activeTurnState resolves the strip while a turn runs: retry, tool,
// synthesizing, responding, reasoning, thinking — in that priority.
func (m Model) activeTurnState() (string, kit.Token) {
	switch {
	case m.retryState != nil:
		return fmt.Sprintf("↻ retrying %d/%d · %s",
			m.retryState.Attempt, m.retryState.Retries, shortDur(m.retryState.DelayMs)), kit.Warn
	case m.activeTool != "":
		return "◆ " + m.activeTool + " — " + argSummary(m.activeTool, m.activeArgs), kit.Tool
	case m.synthDone != nil:
		return "◆ " + synthMark(m.synthDone) + " · working", kit.Accent
	case m.streamedText:
		return "● responding", kit.Accent
	case m.reasoned:
		return "◐ reasoning", kit.Accent
	default:
		return "● thinking", kit.Accent
	}
}

// retryInfo snapshots one announced provider retry.
type retryInfo struct {
	Attempt, Retries, DelayMs int
}

// toolDone remembers the last ToolCallEnd while the turn waits for the
// model's follow-up — the synthesizing state's summary line.
type toolDone struct {
	Tool string
	OK   bool
}

// synthMark renders the synthesizing summary: the finished tool's outcome
// glyph and name.
func synthMark(d *toolDone) string {
	mark := "✓"
	if !d.OK {
		mark = "✗"
	}

	return mark + " " + d.Tool
}

// shortDur renders a backoff milliseconds count compactly (4s, 1.5s).
func shortDur(ms int) string {
	if ms%1000 == 0 {
		return fmt.Sprintf("%ds", ms/1000)
	}

	return fmt.Sprintf("%.1fs", float64(ms)/1000)
}

// usageSummary renders the last turn's token counts, or "" before the
// first turn reports usage.
func usageSummary(u golemclient.Usage) string {
	if u.InputTokens == 0 && u.OutputTokens == 0 {
		return ""
	}

	return fmt.Sprintf(" · ↑%s ↓%s", tokenCount(u.InputTokens), tokenCount(u.OutputTokens))
}

// tokenCount shortens a token count (1.2k, 15k, 2.1M).
func tokenCount(n int64) string {
	switch {
	case n >= 1_000_000:
		return fmt.Sprintf("%.1fM", float64(n)/1_000_000)
	case n >= 1_000:
		return fmt.Sprintf("%.1fk", float64(n)/1_000)
	default:
		return fmt.Sprintf("%d", n)
	}
}

// stickyText returns the question governing the visible region — the
// latest user message whose block starts above the viewport's bottom
// line, i.e. the question the content on screen is answering — or ""
// when that question is already fully visible (or none exists).
func (m Model) stickyText() string {
	bottom := m.vp.YOffset + m.vp.Height

	governing := -1

	for i, span := range m.userSpans {
		if span.start < bottom {
			governing = i
		}
	}

	if governing < 0 {
		return ""
	}

	span := m.userSpans[governing]
	if span.start >= m.vp.YOffset && span.end <= bottom {
		return "" // fully visible: the transcript itself shows it
	}

	return span.text
}

// shrunkViewport renders the transcript window under a pinned sticky
// line, one row shorter so the total screen height stays constant.
func (m Model) shrunkViewport() string {
	offset, height := m.renderedWindow()

	vp := m.vp
	vp.YOffset, vp.Height = offset, height

	return vp.View()
}

// renderedWindow returns the content-line window [offset, offset+height)
// the transcript actually renders: the sticky bar shortens the viewport
// by one row, and a window pinned to the tail must advance by that row —
// pinning against the full height alone strands the last content line
// below the fold, hidden behind the composer box. A scrolled-back window
// stays top-aligned: it only loses its final row.
func (m Model) renderedWindow() (offset, height int) {
	height = max(m.vp.Height-stickyRows, viewportMinHeight)
	offset = m.vp.YOffset

	if m.vp.AtBottom() {
		offset = max(m.totalLines()-height, 0)
	}

	return offset, height
}

// totalLines counts the rendered transcript's content lines.
func (m Model) totalLines() int {
	return len(strings.Split(m.content, "\n"))
}

// stickyRows is the row count the pinned sticky header borrows from the
// viewport.
const stickyRows = 1

// modalized renders the modal floating over a faded, escape-free backdrop
// of the full layout — the base stays visible in the theme's dim tone.
func (m Model) modalized(modal string) string {
	backdrop := m.kit.Fade(ansi.Strip(m.layout()))

	return m.kit.Overlay(m.width, m.height, backdrop, modal)
}

// modelsBody renders the model picker list.
func (m Model) modelsBody() string {
	if len(m.models) == 0 {
		current := m.modelName
		if current == "" {
			current = "unknown"
		}

		return current + "\n" + m.kit.DimLine("no model list — enter closes")
	}

	var b strings.Builder

	for i, model := range m.models {
		fmt.Fprintf(&b, "%s\n", m.kit.Row(model, i == m.modelCursor)) //nolint:errcheck // strings.Builder
	}

	b.WriteString(m.kit.DimLine("↑↓ select · enter apply · esc cancel"))

	return b.String()
}

// sessionsBody renders the session picker: a scrollable window over
// date-grouped rows (opencode's shape — Today/Yesterday/… headers),
// the live query, and the key hints. Rows carry a · current marker for
// the session this client is in and · running for sessions with a live
// turn. The window is capped so the modal never outgrows the screen;
// the cursor (or the mouse wheel) drives the scroll.
func (m Model) sessionsBody() string {
	var b strings.Builder

	lines := m.pickerLines()

	if len(lines) == 0 {
		b.WriteString(m.kit.DimLine("no matches") + "\n")
	}

	for _, ln := range lines {
		fmt.Fprintf(&b, "%s\n", ln.render) //nolint:errcheck // strings.Builder
	}

	// The search field: without it nothing shows what the user typed —
	// rows narrowing is the only feedback, and a typo has no visible
	// correction. Mirrors the command palette's prompt line.
	b.WriteString("\n" + m.kit.Prompt("› "+m.sessQuery+"_") + "\n")
	b.WriteString(m.kit.DimLine("type to filter · ctrl+r rename · ctrl+d delete · enter open · esc cancel"))

	return b.String()
}

// pickerLine is one rendered line of the picker list: either a date
// group header or a session row.
type pickerListLine struct {
	render    string
	sessionID string // "" for headers
	rowIndex  int    // index into filteredSessions; -1 for headers
}

// pickerLines builds the flat line list (headers + rows) and windows it
// to the visible budget around the stored scroll. Pure: the scroll
// itself moves only in update paths (scrollToCursor, the wheel).
func (m Model) pickerLines() []pickerListLine {
	filtered := m.filteredSessions()
	now := time.Now()

	var lines []pickerListLine

	lastGroup := ""
	for i, s := range filtered {
		if label, ok := dateGroupLabel(now, s.LastActive); ok && label != lastGroup {
			lines = append(lines, pickerListLine{
				render: m.kit.DimLine(label), sessionID: "", rowIndex: -1,
			})
			lastGroup = label
		}

		row := m.sessionRow(s, now, m.sessRowWidth(), i == m.pickIndex)

		if m.sessDeleteArmed == s.ID {
			row = m.sessDeleteRow(s, m.sessRowWidth(), i == m.pickIndex)
		}

		if i == m.pickIndex && m.sessRenaming {
			row = m.kit.Prompt("› " + m.sessRenameText + "_")
		}

		lines = append(lines, pickerListLine{
			render: m.kit.Row(row, i == m.pickIndex), sessionID: s.ID, rowIndex: i,
		})
	}

	start := min(max(m.sessScroll, 0), max(len(lines)-m.pickerVisible(), 0))
	end := min(start+m.pickerVisible(), len(lines))

	return lines[start:end]
}

// scrollPicker moves the picker's window freely (the wheel does not
// move the cursor) and clamps to the line range.
func (m *Model) scrollPicker(delta int) {
	total := m.pickerLineCount()
	m.sessScroll = min(max(m.sessScroll+delta, 0), max(total-m.pickerVisible(), 0))
}

// pickerLineCount is the flat line count the picker would render:
// session rows plus one header per date-group boundary.
func (m Model) pickerLineCount() int {
	filtered := m.filteredSessions()
	if len(filtered) == 0 {
		return 0
	}

	now := time.Now()
	lines := 0
	lastGroup := ""

	for _, s := range filtered {
		if label, ok := dateGroupLabel(now, s.LastActive); ok && label != lastGroup {
			lines++
			lastGroup = label
		}

		lines++
	}

	return lines
}

// scrollToCursor recenters the picker window so the cursor's line
// (header lines included) is visible — called wherever pickIndex moves
// or the list shrinks (delete, filter). Mirrors pickerLines' grouping
// exactly: a header line precedes a row whenever its group label
// differs from the previous row's (including the first row, whose
// implicit predecessor is no group at all).
func (m *Model) scrollToCursor() {
	filtered := m.filteredSessions()
	if len(filtered) == 0 {
		m.sessScroll = 0

		return
	}

	now := time.Now()

	lines := 0
	cursorLine := 0
	groupTop := 0 // the cursor's group header line (or its own row)
	lastGroup := ""
	headered := false

	for i, s := range filtered {
		headered = false

		if label, ok := dateGroupLabel(now, s.LastActive); ok && label != lastGroup {
			lines++
			lastGroup = label
			headered = true
		}

		if i == m.pickIndex {
			cursorLine = lines

			// The group's header sits one line above its first row
			// whenever one was emitted for this group; scrolling up
			// lands there so a group enters view with its label.
			if headered {
				groupTop = lines - 1
			} else {
				groupTop = lines
			}
		}

		lines++
	}

	switch {
	case cursorLine < m.sessScroll:
		m.sessScroll = groupTop
	case cursorLine >= m.sessScroll+m.pickerVisible():
		m.sessScroll = cursorLine - m.pickerVisible() + 1
	}

	m.sessScroll = min(max(m.sessScroll, 0), max(lines-m.pickerVisible(), 0))
}

// pickerVisible is the line budget for the picker window, clamped to
// the terminal: the modal (title + query + hints + frame) needs room,
// and a short terminal shrinks the window before the modal overflows.
func (m Model) pickerVisible() int {
	// Modal chrome: title row, blank row, query row, hints row, plus
	// the border/padding frame rows.
	const modalFixedRows = 8

	return max(min(pickerVisibleRows, m.height-modalFixedRows), 1)
}

// dateGroupLabel buckets a session's last-active time into a section
// label: Today, Yesterday, then the age (3d ago…) or the calendar date
// for anything older than a week — relTime's buckets, section-sized.
// Sessions missing a parsable timestamp join no group (ok=false) and
// render headerless, keeping the newest-first flow intact.
func dateGroupLabel(now time.Time, rfc3339 string) (string, bool) {
	ts, err := time.Parse(time.RFC3339, rfc3339)
	if err != nil {
		return "", false
	}

	diff := now.Sub(ts)

	switch {
	case diff < 24*time.Hour && now.Day() == ts.Day():
		return "Today", true
	case diff < 48*time.Hour:
		return "Yesterday", true
	case diff < 7*24*time.Hour:
		return fmt.Sprintf("%dd ago", int(diff.Hours()/24)), true
	default:
		return ts.Format("Jan 2"), true
	}
}

// rowBar renders the bar column: the current-session marker, plain
// when focused (reverse video inverts nested colors into blocks).
func (m Model) rowBar(sessionID string, focused bool) string {
	if sessionID != m.sessionID {
		return strings.Repeat(" ", sessBarCols)
	}

	if focused {
		return "▎ "
	}

	return m.kit.Marker(kit.User, "▎ ")
}

// rowStatus renders the status column: the running-turn spinner, plain
// when focused.
func (m Model) rowStatus(s golemclient.SessionSummary, focused bool) string {
	if !m.sessionRunning(s) {
		return strings.Repeat(" ", sessStatusCols)
	}

	glyph := kit.SpinnerFrames[(m.sessSpinFrame%len(kit.SpinnerFrames)+len(kit.SpinnerFrames))%len(kit.SpinnerFrames)]

	if focused {
		return glyph + " "
	}

	return m.kit.Spinner(m.sessSpinFrame) + " "
}

// sessDeleteHint is the armed row's confirm prompt.
const sessDeleteHint = "ctrl+d again to confirm"

// sessDeleteRow renders the armed row: same bar and status columns, the
// title replaced by the confirm hint in the error token — one
// destructive cue per opencode's press-again pattern, columns aligned.
// focused rows render plain (see sessionRow: nested colors invert into
// blocks under the reverse-video highlight).
func (m Model) sessDeleteRow(s golemclient.SessionSummary, width int, focused bool) string {
	bar := m.rowBar(s.ID, focused)
	status := m.rowStatus(s, focused)

	hintCols := max(width-sessBarCols-sessStatusCols, 1)

	if focused {
		return bar + status + ansi.Truncate(sessDeleteHint, hintCols, "…")
	}

	return bar + status + m.kit.Marker(kit.Error, ansi.Truncate(sessDeleteHint, hintCols, "…"))
}

// Session-row geometry: the current-session bar (one cell plus a space),
// the status column (the spinner glyph or its blank), the title gap
// before the right-aligned age, and the row indent under the cursor.
const (
	sessBarCols    = 2 // "▎ "
	sessStatusCols = 2 // glyph + space
	sessAgeGap     = 2 // spaces between title and age
)

// pickerVisibleRows caps how many list lines (rows + group headers) the
// picker renders — opencode's dialog maxHeight. Beyond it the list
// scrolls inside a fixed window; the modal never outgrows the screen
// and the query line plus key hints stay pinned at the bottom.
const pickerVisibleRows = 15

// sessRowWidth is the cell budget one picker row may span: the modal
// body's inner width (viewport minus the frame) minus the shared
// two-cell gutter (one on each flank, every row alike). The floor keeps
// a narrow terminal readable.
func (m Model) sessRowWidth() int {
	return max(m.vp.Width-modalChromeTotal-sessGutterCols, 20)
}

// modalChromeTotal mirrors kit's modal frame cost (two border columns
// plus horizontal padding on both flanks) for width budgeting here.
const modalChromeTotal = 6

// sessGutterCols is the gutter every row spends: one cell per flank
// (kit.RowGutter), cursor row and plain rows alike.
const sessGutterCols = 2 * kit.RowGutter

// sessionRow renders one picker row as aligned visual columns —
//
//	▎ ⠋ Fix auth middleware    2h ago
//
// the bar marks the session this client is in, the spinner (animated by
// sessTickMsg) marks a live turn, and the dim age stays right. No text
// markers: state reads from color and glyphs, not words. The raw
// session id never shows.
//
// focused rows render plain on purpose: the focus highlight is reverse
// video (kit.Row), and any colored span nested inside it has its
// foreground inverted into a background — the bar would become a bright
// block, the spinner and age colored rectangles. Glyphs alone carry the
// state while focused; color returns when the cursor moves off.
func (m Model) sessionRow(s golemclient.SessionSummary, now time.Time, width int, focused bool) string {
	name := s.Title

	if name == "" {
		name = dirName(s.Cwd)

		if name == "" {
			name = s.ID
		}
	}

	bar := m.rowBar(s.ID, focused)
	status := m.rowStatus(s, focused)

	age := ""
	if rel, ok := relTime(now, s.LastActive); ok {
		age = rel
	}

	// The title absorbs truncation: fixed columns (bar, status, age)
	// never lose cells to a long title.
	titleCols := max(width-sessBarCols-sessStatusCols-len(age)-sessAgeGap, 1)
	name = ansi.Truncate(name, titleCols, "…")

	if focused {
		return bar + status + name + strings.Repeat(" ", sessAgeGap) + age
	}

	return bar + status + name + strings.Repeat(" ", sessAgeGap) + m.kit.DimLine(age)
}

// sessionRunning reports whether a turn is live in the listed session.
// The server flag is a list-time snapshot; for the session this client is
// in, the local turn state is fresher and overrides it.
func (m Model) sessionRunning(s golemclient.SessionSummary) bool {
	if s.ID == m.sessionID {
		return m.turnActive
	}

	return s.Busy
}

// dirName is the final path segment of a working directory.
func dirName(cwd string) string {
	if cwd == "" {
		return ""
	}

	return filepath.Base(cwd)
}

// relTime renders a timestamp as a short relative age ("2h ago"). It
// reports false when the timestamp is missing or unparsable.
func relTime(now time.Time, rfc3339 string) (string, bool) {
	ts, err := time.Parse(time.RFC3339, rfc3339)
	if err != nil {
		return "", false
	}

	diff := now.Sub(ts)

	switch {
	case diff < time.Minute:
		return "just now", true
	case diff < time.Hour:
		return fmt.Sprintf("%dm ago", int(diff.Minutes())), true
	case diff < 24*time.Hour:
		return fmt.Sprintf("%dh ago", int(diff.Hours())), true
	case diff < 7*24*time.Hour:
		return fmt.Sprintf("%dd ago", int(diff.Hours()/24)), true
	default:
		return ts.Format("Jan 2"), true
	}
}
