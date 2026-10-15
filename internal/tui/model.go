package tui

import (
	"github.com/charmbracelet/bubbles/textarea"
	"github.com/charmbracelet/bubbles/viewport"
	tea "github.com/charmbracelet/bubbletea"

	"github.com/dev-hann/golem/internal/tui/kit"
	"github.com/dev-hann/golem/internal/tui/markdown"
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
	exitArmed       bool // ctrl+c once on an idle empty composer — awaiting the confirm press
	exitGen         int  // exit-confirm generation (stale-tick guard)
	cmdQuery        string
	cmdCursor       int
	sessions        []golemclient.SessionSummary
	pickIndex       int
	sessQuery       string
	spinFrame       int // animation frame (picker spinner rows, status strip glyph)
	spinGen         int // animation ticker generation (stale-tick guard)
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
	md              *markdown.Renderer // assistant markdown renderer (ADR-0026)
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
		md:        newMarkdownRenderer(),
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
		return m.timeoutLeader(msg)
	case exitTimeoutMsg:
		return m.timeoutExit(msg)
	case spinTickMsg:
		return m.spinTick(msg)
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

// ConnErr reports the connection error that ended the session, if any.
func (m Model) ConnErr() string {
	return m.connErr
}
