package tui

import (
	"fmt"
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
	modeHelp
	modeModels
)

// Model is the Bubble Tea model for the golem terminal client.
type Model struct {
	client  client
	dial    dialer
	socket  string
	connErr string
	kit     *kit.Kit

	mode        mode
	sessionID   string
	turnActive  bool
	follow      bool
	blocks      []block
	userSpans   []userSpan
	leaderArmed bool
	leaderGen   int
	pending     *golemclient.ApprovalRequest
	sessions    []golemclient.SessionSummary
	pickIndex   int
	vp          viewport.Model
	input       textarea.Model
	width       int
	height      int
	provider    string
	modelName   string
	models      []string
	modelCursor int
	statusParts []string
	inputRows   int
	quitting    bool
	listRequest bool
}

// New builds the model for a socket path.
func New(socketPath string) Model {
	return NewThemed(socketPath, kit.New(kit.DefaultTheme()))
}

// NewThemed builds the model with an explicit kit (theme switches
// construct a new model view through this).
func NewThemed(socketPath string, k *kit.Kit) Model {
	input := textarea.New()
	input.Placeholder = "message… (enter send · alt+enter newline · ctrl+x keys)"
	input.ShowLineNumbers = false
	input.FocusedStyle.Text = k.Style(kit.Fg)
	input.FocusedStyle.Placeholder = k.Style(kit.Dim)
	input.BlurredStyle = input.FocusedStyle
	input.SetHeight(minInputHeight)
	input.SetWidth(startWidth - inputBorderCols)
	input.Focus()

	vp := viewport.New(startWidth, startHeight-headerLines-footerLines-minInputHeight-inputBorderRows)

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

// sessionIDShortLen bounds the session id shown in the status bar.
const sessionIDShortLen = 8

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
	headerLines              = 1
	footerLines              = 1
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
			m.leaderArmed = false
		}

		return m, nil
	case tea.MouseMsg:
		m.mouseScroll(msg)
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
	case modeHelp:
		m.mode = modeChat // any key closes the help modal

		return m, nil
	case modeModels:
		return m.updateModels(key)
	default:
		return m, nil
	}
}

// ctrlC interrupts an active turn; a second press (or idle press) quits.
func (m Model) ctrlC() (tea.Model, tea.Cmd) {
	if m.turnActive && m.client != nil {
		m.statusParts = append(m.statusParts[:len(m.statusParts):len(m.statusParts)], "interrupting…")

		return m, m.send(golemclient.Interrupt{
			Type: golemclient.TypeInterrupt, Session: m.sessionID,
		})
	}

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
		m.leaderArmed = true
		m.leaderGen++

		return m, armLeader(m.leaderGen)
	case tea.KeyEscape:
		return m.escKey()
	case tea.KeyEnter:
		if key.Alt { // alt/option+enter inserts a newline; plain enter submits
			m.input.InsertString("\n")
			m.syncInputHeight()

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

// leaderHints is the cheat sheet the status bar shows while a ctrl+x
// sequence is pending.
const leaderHints = "n new · p sessions · m model · x expand · l clear · ? help · esc cancel"

// leaderKey dispatches the second key of a ctrl+x sequence. Bound keys
// consume the press; esc cancels; anything else disarms and continues as
// ordinary chat input, so a stray arm never swallows typing (ctrl+x here
// re-arms with a fresh window). Direct chords stay impossible where the
// terminal protocol cannot distinguish them — ctrl+m is enter (CR), which
// is why every command rides the leader namespace.
func (m Model) leaderKey(key tea.KeyMsg) (tea.Model, tea.Cmd) {
	m.leaderArmed = false

	if key.Type == tea.KeyEscape {
		return m, nil
	}

	if !key.Alt && key.Type == tea.KeyRunes && len(key.Runes) == 1 {
		switch key.Runes[0] {
		case 'n':
			m.blocks = nil
			m.statusParts = nil
			m.provider, m.modelName = "", ""
			m.turnActive = false
			m.refreshView()

			return m, m.send(golemclient.CreateSession{Type: golemclient.TypeCreateSession})
		case 'p':
			m.listRequest = true

			return m, m.send(golemclient.ListSessions{Type: golemclient.TypeListSessions})
		case 'm':
			m.mode = modeModels
			m.modelCursor = m.cursorOnModel(m.modelName)

			return m, nil
		case 'x':
			m.toggleLastTool()
			m.refreshView()

			return m, nil
		case 'l':
			m.blocks = nil
			m.refreshView()

			return m, nil
		case '?':
			m.mode = modeHelp

			return m, nil
		}
	}

	return m.updateChat(key)
}

// escKey interrupts an active turn; idle presses do nothing (quitting is
// ctrl+c's job).
func (m Model) escKey() (tea.Model, tea.Cmd) {
	if m.turnActive {
		return m.ctrlC()
	}

	return m, nil
}

// toggleLastTool expands or collapses the most recent tool block.
func (m *Model) toggleLastTool() {
	for i := len(m.blocks) - 1; i >= 0; i-- {
		if m.blocks[i].kind == blockTool {
			m.blocks[i].expanded = !m.blocks[i].expanded
			return
		}
	}
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

// scrollKey handles page-scroll keys, reporting whether it consumed the key.
func (m *Model) scrollKey(key tea.KeyMsg) bool {
	switch key.Type { //nolint:exhaustive // only the scroll keys are ours
	case tea.KeyPgUp:
		m.scrollBy(-pageLines)

		return true
	case tea.KeyPgDown:
		m.scrollBy(pageLines)

		return true
	default:
		return false
	}
}

// mouseScroll turns wheel motion into transcript scrolling through the
// shared follow-suspension path.
func (m *Model) mouseScroll(msg tea.MouseMsg) {
	switch msg.Button { //nolint:exhaustive // only the wheel scrolls
	case tea.MouseButtonWheelUp:
		m.scrollBy(-wheelLines)
	case tea.MouseButtonWheelDown:
		m.scrollBy(wheelLines)
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
	case tea.KeyUp:
		m.modelCursor = max(m.modelCursor-1, 0)
	case tea.KeyDown:
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

// updateSessions handles the session picker.
func (m Model) updateSessions(key tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch key.Type { //nolint:exhaustive // foreign enum; default ignores
	case tea.KeyEsc:
		m.mode = modeChat
		return m, nil
	case tea.KeyUp:
		if m.pickIndex > 0 {
			m.pickIndex--
		}

		return m, nil
	case tea.KeyDown:
		if m.pickIndex < len(m.sessions)-1 {
			m.pickIndex++
		}

		return m, nil
	case tea.KeyEnter:
		if len(m.sessions) == 0 {
			m.mode = modeChat
			return m, nil
		}

		chosen := m.sessions[m.pickIndex]
		m.mode = modeChat
		m.blocks = nil

		return m, m.send(golemclient.LoadSession{
			Type: golemclient.TypeLoadSession, Session: chosen.ID,
		})
	default:
		return m, nil
	}
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

// resize recomputes layout on terminal size changes.
func (m *Model) resize(width, height int) {
	m.width, m.height = width, height
	m.vp.Width = max(width, viewportMinWidth)
	m.vp.Height = max(height-m.inputRows-inputBorderRows-headerLines-footerLines, viewportMinHeight)
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
		m.vp.Height = max(m.height-rows-inputBorderRows-headerLines-footerLines, viewportMinHeight)
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

		short := e.Session
		if len(short) > sessionIDShortLen {
			short = short[:sessionIDShortLen]
		}

		m.statusParts = []string{"session " + short, fmt.Sprintf("%d turns", e.Turns)}
	case golemclient.TurnStart:
		m.turnActive = true
		m.follow = true
		m.provider = e.Provider
		m.modelName = e.Model
		m.statusParts = append(m.statusParts, "thinking…")
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
		m.statusParts = append(m.statusParts[:len(m.statusParts):len(m.statusParts)],
			fmt.Sprintf("done (in=%d out=%d)", e.Usage.InputTokens, e.Usage.OutputTokens))
	case golemclient.SessionList:
		m.sessions = e.Sessions

		m.pickIndex = 0
		if m.listRequest {
			m.listRequest = false
			m.mode = modeSessions
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
	content, spans := renderBlocks(m.kit, m.blocks, m.vp.Width)
	m.userSpans = spans
	m.vp.SetContent(content)

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
	case modeHelp:
		return m.modalized(m.kit.Modal("keys", m.helpBody()))
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
	b.WriteString(header(m) + "\n")

	if text := m.stickyText(); text != "" {
		b.WriteString(m.kit.StickyUser(m.vp.Width, text) + "\n")
		b.WriteString(m.shrunkViewport() + "\n")
	} else {
		b.WriteString(m.vp.View() + "\n")
	}

	b.WriteString(m.footer() + "\n")
	b.WriteString(m.kit.Box(m.input.View()))

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

// helpBody renders the keybinding reference.
func (m Model) helpBody() string {
	rows := [][2]string{
		{"enter", "send message"},
		{"alt+enter", "newline in message"},
		{"ctrl+x n", "new session"},
		{"ctrl+x p", "session picker"},
		{"ctrl+x m", "model picker"},
		{"ctrl+x x", "expand or collapse tool block"},
		{"ctrl+x l", "clear transcript view"},
		{"ctrl+x ?", "this help"},
		{"esc / ctrl+c", "interrupt turn (ctrl+c twice quits)"},
		{"pgup / pgdn", "scroll transcript"},
	}

	var b strings.Builder
	for _, row := range rows {
		fmt.Fprintf(&b, "%s%-14s %s\n", "", row[0], row[1]) //nolint:errcheck // strings.Builder
	}

	b.WriteString(m.kit.DimLine("any key closes"))

	return b.String()
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

// sessionsBody renders the session picker list.
func (m Model) sessionsBody() string {
	var b strings.Builder

	for i, s := range m.sessions {
		line := fmt.Sprintf("%s %s (%d turns)", s.ID, s.Cwd, s.Turns)
		if i == m.pickIndex {
			b.WriteString(m.kit.Selection("▶ "+line) + "\n")

			continue
		}

		b.WriteString("  " + line + "\n")
	}

	b.WriteString(m.kit.DimLine("↑↓ select · enter open · esc cancel"))

	return b.String()
}

// approvalBody renders the approval prompt.
func (m Model) approvalBody() string {
	return m.kit.WarnLine("run this command?") + "\n" +
		m.kit.ToolLine("  "+m.pending.Preview) + "\n" +
		m.kit.DimLine("y allow · n deny")
}

// header renders the top line.
func header(m Model) string {
	state := "ready"
	if m.turnActive {
		state = "busy"
	}

	return fmt.Sprintf("golem — %s (%s)", m.sessionID, state)
}

// footer renders the status bar line.
func (m Model) footer() string {
	parts := m.statusParts
	if m.leaderArmed {
		parts = append([]string{leaderHints}, parts...)
	}

	if m.provider != "" {
		parts = append([]string{m.provider, m.modelName}, parts...)
	}

	return m.kit.StatusBar(parts...)
}
