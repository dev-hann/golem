package tui

import (
	"fmt"
	"strings"

	"github.com/charmbracelet/bubbles/textarea"
	"github.com/charmbracelet/bubbles/viewport"
	tea "github.com/charmbracelet/bubbletea"

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
	blocks      []block
	pending     *golemclient.ApprovalRequest
	sessions    []golemclient.SessionSummary
	pickIndex   int
	vp          viewport.Model
	input       textarea.Model
	width       int
	height      int
	provider    string
	modelName   string
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
	input.Placeholder = "message… (enter send · alt+enter newline · ctrl+c interrupt)"
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
	switch key.Type { //nolint:exhaustive // foreign enum; default routes to the textarea
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
	case tea.KeyPgUp:
		m.vp.ScrollUp(pageLines)
		return m, nil
	case tea.KeyPgDown:
		m.vp.ScrollDown(pageLines)
		return m, nil
	case tea.KeyTab:
		m.toggleLastTool()
		m.refreshView()

		return m, nil
	default:
		var cmd tea.Cmd

		m.input, cmd = m.input.Update(key)
		m.syncInputHeight()

		return m, cmd
	}
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

// submit sends a chat line or local command, returning the updated model so
// transcript mutations survive the value-receiver chain.
func (m Model) submit(line string) (tea.Model, tea.Cmd) {
	switch line {
	case "/quit", "/exit":
		m.quitting = true

		return m, tea.Quit
	case "/sessions":
		m.listRequest = true

		return m, m.send(golemclient.ListSessions{Type: golemclient.TypeListSessions})
	default:
		if m.client == nil || m.sessionID == "" {
			m.blocks = append(m.blocks, block{kind: blockError, text: "not connected yet"})
			m.refreshView()

			return m, nil
		}

		m.blocks = append(m.blocks, block{kind: blockUser, text: line})
		m.refreshView()

		return m, m.send(golemclient.UserMessage{
			Type: golemclient.TypeUserMessage, Session: m.sessionID, Text: line,
		})
	}
}

// updateApproval handles y/n for a pending approval_request.
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
func (m *Model) applyEvent(ev any) tea.Cmd {
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
	case golemclient.Envelope:
		// unknown types ignored per PROTOCOL.md
	default:
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

// refreshView pushes the rendered transcript into the viewport and pins to
// the bottom while a turn streams.
func (m *Model) refreshView() {
	m.vp.SetContent(renderBlocks(m.kit, m.blocks))

	if m.turnActive {
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

	var b strings.Builder
	b.WriteString(header(m) + "\n")
	b.WriteString(m.vp.View() + "\n")
	b.WriteString(m.footer() + "\n")
	b.WriteString(m.kit.Box(m.input.View()))

	return b.String()
}

// header renders the top line.
func header(m Model) string {
	state := "ready"
	if m.turnActive {
		state = "busy"
	}

	return fmt.Sprintf("golem — %s (%s)", m.sessionID, state)
}

// footer renders the mode footer (approval prompt, session picker, or
// status bar) through the kit theme.
func (m Model) footer() string {
	if m.mode == modeApproval && m.pending != nil {
		return m.kit.WarnLine("run command? y = allow, n = deny") + "\n" +
			m.kit.ToolLine("  → "+m.pending.Preview)
	}

	if m.mode == modeSessions {
		var b strings.Builder
		b.WriteString(m.kit.Title("sessions"))
		b.WriteString(m.kit.DimLine(" — ↑/↓ select, enter open, esc cancel\n"))

		for i, s := range m.sessions {
			line := fmt.Sprintf("%s %s (%d turns)", s.ID, s.Cwd, s.Turns)
			if i == m.pickIndex {
				b.WriteString(m.kit.Selection(line))
			} else {
				b.WriteString("  " + line)
			}

			b.WriteString("\n")
		}

		return b.String()
	}

	parts := m.statusParts
	if m.provider != "" {
		parts = append([]string{m.provider, m.modelName}, parts...)
	}

	return m.kit.StatusBar(parts...)
}
