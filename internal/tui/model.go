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
	modeApproval
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

	mode           mode
	sessionID      string
	turnActive     bool
	follow         bool
	blocks         []block
	userSpans      []userSpan
	toolSpans      []toolSpan
	leaderArmed    bool
	leaderGen      int
	cmdQuery       string
	cmdCursor      int
	pending        *golemclient.ApprovalRequest
	sessions       []golemclient.SessionSummary
	pickIndex      int
	sessQuery      string
	sessRenaming   bool
	sessRenameText string
	vp             viewport.Model
	input          textarea.Model
	width          int
	height         int
	content        string // plain rendered transcript; the copy source
	selecting      bool   // a left-button drag over the transcript is in progress
	selAnchor      int    // content line where the drag began
	selHead        int    // content line under the drag's pointer
	copier         func(string) error
	provider       string
	modelName      string
	models         []string
	modelCursor    int
	inputRows      int
	quitting       bool
	listRequest    bool
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

// connect dials the server and creates a session.
func (m Model) connect() tea.Cmd {
	c := m

	return func() tea.Msg {
		conn, err := c.dial(c.socket)
		if err != nil {
			return connErrMsg{err: err}
		}

		if err := conn.Send(golemclient.CreateSession{
			Type: golemclient.TypeCreateSession,
		}); err != nil {
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
	case modeApproval:
		return m.updateApproval(key)
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
// session over the same connection.
func (m Model) newSessionCmd() (tea.Model, tea.Cmd) {
	m.clearSelection()
	m.blocks = nil
	m.provider, m.modelName = "", ""
	m.turnActive = false
	m.refreshView()

	return m, m.send(golemclient.CreateSession{Type: golemclient.TypeCreateSession})
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
		{name: "quit", keys: "ctrl+d", run: Model.quitCmd},
	}
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
		if i == m.cmdCursor {
			fmt.Fprintf(&b, "%s\n", m.kit.Selection("▶ "+row)) //nolint:errcheck // strings.Builder

			continue
		}

		fmt.Fprintf(&b, "  %s\n", row) //nolint:errcheck // strings.Builder
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

// handleMouse routes mouse input: the wheel scrolls the transcript, a
// left press toggles the tool row under it or begins a text selection,
// drags extend the selection, and the release finishes it.
func (m *Model) handleMouse(msg tea.MouseMsg) {
	switch {
	case msg.Button == tea.MouseButtonWheelUp:
		m.scrollBy(-wheelLines)
	case msg.Button == tea.MouseButtonWheelDown:
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

// submit sends the composed line as a chat message. Everything typed is
// content — navigation and lifecycle live on keys, not slash commands.
func (m Model) submit(line string) (tea.Model, tea.Cmd) {
	if m.client == nil || m.sessionID == "" {
		m.blocks = append(m.blocks, block{kind: blockError, text: "not connected yet"})
		m.refreshView()

		return m, nil
	}

	m.blocks = append(m.blocks, block{kind: blockUser, text: line})
	m.follow = true
	m.refreshView()

	return m, m.send(golemclient.UserMessage{
		Type: golemclient.TypeUserMessage, Session: m.sessionID, Text: line,
	})
}

// updateApproval handles y/n for a pending approval_request.
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

func (m Model) updateApproval(key tea.KeyMsg) (tea.Model, tea.Cmd) {
	req := m.pending
	if req == nil {
		m.mode = modeChat
		return m, nil
	}

	decision := "deny"
	if key.Type == tea.KeyRunes && (key.String() == "y" || key.String() == "Y") {
		decision = "allow"
	}

	m.mode = modeChat
	m.pending = nil

	return m, m.send(golemclient.ApprovalResponse{
		Type: golemclient.TypeApprovalResponse, Session: req.Session,
		Turn: req.Turn, ApprovalID: req.ApprovalID, Decision: decision,
	})
}

// updateSessions drives the session picker: typing filters (fuzzy over
// title and directory), ctrl+r renames the highlighted row inline, and
// the dialog keys move/confirm. esc clears the query first, then closes.
func (m Model) updateSessions(key tea.KeyMsg) (tea.Model, tea.Cmd) {
	if m.sessRenaming {
		return m.renameKey(key)
	}

	filtered := m.filteredSessions()

	switch key.Type { //nolint:exhaustive // foreign enum; default filters
	case tea.KeyEsc:
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
	case tea.KeyUp, tea.KeyCtrlP:
		m.pickIndex = max(m.pickIndex-1, 0)

		return m, nil
	case tea.KeyDown, tea.KeyCtrlN:
		m.pickIndex = min(m.pickIndex+1, len(filtered)-1)

		return m, nil
	case tea.KeyBackspace:
		if runes := []rune(m.sessQuery); len(runes) > 0 {
			m.sessQuery = string(runes[:len(runes)-1])
			m.pickIndex = min(m.pickIndex, max(len(m.filteredSessions())-1, 0))
		}

		return m, nil
	case tea.KeyEnter:
		if len(filtered) == 0 {
			return m, nil
		}

		chosen := filtered[m.pickIndex]
		m.mode = modeChat
		m.blocks = nil

		return m, m.send(golemclient.LoadSession{
			Type: golemclient.TypeLoadSession, Session: chosen.ID,
		})
	case tea.KeyRunes:
		if !key.Alt {
			m.sessQuery += string(key.Runes)
			m.pickIndex = min(m.pickIndex, max(len(m.filteredSessions())-1, 0))
		}

		return m, nil
	default:
		return m, nil
	}
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
	m.vp.Width = max(width, viewportMinWidth)
	m.vp.Height = max(height-m.inputRows-inputBorderRows, viewportMinHeight)
	m.input.SetWidth(max(width-inputBorderCols, viewportMinWidth))
	m.syncInputHeight()
}

// syncInputHeight grows the input box with its content — one row per
// newline, min minInputHeight, capped at the terminal share — and returns
// the freed or claimed rows to the transcript viewport.
func (m *Model) syncInputHeight() {
	rows := strings.Count(m.input.Value(), "\n") + 1

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
		m.vp.Height = max(m.height-rows-inputBorderRows, viewportMinHeight)
	}
}

// Layout minimums so tiny terminals still render.
const (
	viewportMinWidth  = 20
	viewportMinHeight = 3
)

// applyEvent folds one server event into the model, returning the next cmd.
// sessionOf extracts the session a streaming event belongs to; "" for
// events that are session-less or carry a new session identity (SessionMeta).
func sessionOf(ev any) string {
	switch e := ev.(type) {
	case golemclient.TurnStart:
		return e.Session
	case golemclient.TextDelta:
		return e.Session
	case golemclient.ToolCallStart:
		return e.Session
	case golemclient.ToolCallEnd:
		return e.Session
	case golemclient.ApprovalRequest:
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

	switch e := ev.(type) {
	case golemclient.SessionMeta:
		m.sessionID = e.Session
	case golemclient.TurnStart:
		m.turnActive = true
		m.follow = true
		m.provider = e.Provider
		m.modelName = e.Model
	case golemclient.TextDelta:
		m.appendToAssistant(e.Delta)
	case golemclient.ToolCallStart:
		m.blocks = append(m.blocks, block{
			kind: blockTool, callID: e.CallID, tool: e.Tool, args: string(e.Args),
		})
	case golemclient.ToolCallEnd:
		m.finishToolBlock(e)
	case golemclient.ApprovalRequest:
		req := e
		m.pending = &req
		m.mode = modeApproval
	case golemclient.ErrorEvent:
		m.blocks = append(m.blocks, block{kind: blockError, text: e.Message})
		m.turnActive = false
	case golemclient.TurnEnd:
		m.turnActive = false
	case golemclient.SessionList:
		m.applySessionList(e)
	case golemclient.SessionTitle:
		if i := chosenIndex(m.sessions, e.Session); i >= 0 {
			m.sessions[i].Title = e.Title
		}
	case golemclient.History:
		m.replayHistory(e)
	default:
		// Unknown types (including golemclient.Envelope) are ignored per
		// PROTOCOL.md forward compatibility.
	}

	m.refreshView()

	if m.client != nil {
		return m.listen()
	}

	return nil
}

// applySessionList stores a session list and, when it was explicitly
// requested, opens the picker on it with fresh search state.
func (m *Model) applySessionList(e golemclient.SessionList) {
	m.sessions = e.Sessions

	m.pickIndex = 0
	m.sessQuery = ""
	m.sessRenaming = false
	m.sessRenameText = ""

	if m.listRequest {
		m.listRequest = false
		m.mode = modeSessions
	}
}

// appendToAssistant streams deltas into the trailing assistant block.
func (m *Model) appendToAssistant(delta string) {
	if len(m.blocks) == 0 || m.blocks[len(m.blocks)-1].kind != blockAssistant {
		m.blocks = append(m.blocks, block{kind: blockAssistant})
	}

	last := &m.blocks[len(m.blocks)-1]
	last.text += delta
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

// replayHistory rebuilds the transcript from a history event batch.
func (m *Model) replayHistory(hist golemclient.History) {
	m.blocks = nil

	for _, raw := range hist.Events {
		ev, err := golemclient.Decode(raw)
		if err != nil {
			continue
		}

		switch e := ev.(type) {
		case golemclient.UserMessage:
			m.blocks = append(m.blocks, block{kind: blockUser, text: e.Text})
		case golemclient.TextDelta:
			m.appendToAssistant(e.Delta)
		case golemclient.ToolCallStart:
			m.blocks = append(m.blocks, block{
				kind: blockTool, callID: e.CallID, tool: e.Tool, args: string(e.Args),
			})
		case golemclient.ToolCallEnd:
			m.finishToolBlock(e)
		case golemclient.ErrorEvent:
			m.blocks = append(m.blocks, block{kind: blockError, text: e.Message})
		default:
			// turn lifecycle events are implicit in the transcript
		}
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
		return m.modalized(m.kit.Modal("commands", m.commandBody()))
	case modeModels:
		return m.modalized(m.kit.Modal("models · "+m.provider, m.modelsBody()))
	case modeSessions:
		return m.modalized(m.kit.Modal("sessions", m.sessionsBody()))
	case modeApproval:
		if m.pending != nil {
			return m.modalized(m.kit.Modal("approval", m.approvalBody()))
		}
	case modeChat:
		// The full layout below.
	default:
		// modeApproval without a pending request also falls to the layout.
	}

	return m.layout()
}

// layout composes the full chat screen through the active kit.
func (m Model) layout() string {
	var b strings.Builder
	if text := m.stickyText(); text != "" {
		b.WriteString(m.kit.StickyUser(m.vp.Width, text) + "\n")
		b.WriteString(m.shrunkViewport() + "\n")
	} else {
		b.WriteString(m.vp.View() + "\n")
	}

	edge := kit.Border
	if m.leaderArmed {
		edge = kit.Accent
	}

	b.WriteString(m.kit.Box(m.input.View(), edge))

	return b.String()
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

// shrunkViewport renders one viewport row shorter, paying for the sticky
// line so the total screen height stays constant.
func (m Model) shrunkViewport() string {
	vp := m.vp
	vp.Height = max(vp.Height-1, viewportMinHeight)

	return vp.View()
}

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
		if i == m.modelCursor {
			fmt.Fprintf(&b, "%s\n", m.kit.Selection("▶ "+model)) //nolint:errcheck // strings.Builder

			continue
		}

		fmt.Fprintf(&b, "  %s\n", model) //nolint:errcheck // strings.Builder
	}

	b.WriteString(m.kit.DimLine("↑↓ select · enter apply · esc cancel"))

	return b.String()
}

// sessionsBody renders the session picker: matching rows, the live query
// (or the rename input), and the key hints.
func (m Model) sessionsBody() string {
	var b strings.Builder

	filtered := m.filteredSessions()

	if len(filtered) == 0 {
		b.WriteString(m.kit.DimLine("no matches") + "\n")
	}

	for i, s := range filtered {
		if i == m.pickIndex {
			row := sessionRow(s, time.Now())

			if m.sessRenaming {
				row = m.kit.Prompt("› " + m.sessRenameText + "_")
			}

			fmt.Fprintf(&b, "%s\n", m.kit.Selection("▶ "+row)) //nolint:errcheck // strings.Builder

			continue
		}

		fmt.Fprintf(&b, "  %s\n", sessionRow(s, time.Now())) //nolint:errcheck // strings.Builder
	}

	b.WriteString("\n" + m.kit.DimLine("type to filter · ctrl+r rename · enter open · esc cancel"))

	return b.String()
}

// sessionRow renders one picker row: the title (or the working directory
// while untitled) and its date. The raw session id never shows.
func sessionRow(s golemclient.SessionSummary, now time.Time) string {
	name := s.Title

	if name == "" {
		name = dirName(s.Cwd)

		if name == "" {
			name = s.ID
		}
	}

	if rel, ok := relTime(now, s.LastActive); ok {
		return name + " · " + rel
	}

	return name
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

// approvalBody renders the approval prompt.
func (m Model) approvalBody() string {
	return m.kit.WarnLine("run this command?") + "\n" +
		m.kit.ToolLine("  "+m.pending.Preview) + "\n" +
		m.kit.DimLine("y allow · n deny")
}
