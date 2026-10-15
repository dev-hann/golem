package tui

import (
	"charm.land/bubbles/v2/textarea"
	"charm.land/bubbles/v2/viewport"
	tea "charm.land/bubbletea/v2"

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
	compacting      bool              // a compaction's summary side call runs (ADR-0037)
	metaBusy        bool              // the loaded session's live busy flag (session_meta)
	usageIn         int64             // session-cumulative input tokens (meta-restored)
	usageOut        int64             // session-cumulative output tokens (meta-restored)
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
	// The composer self-sizes to its soft-wrapped content (Phase 8.1);
	// golem's terminal-share cap is applied on top in syncInputHeight.
	// MaxHeight keeps the bubbles default: it is also the probe oracle's
	// ceiling, so the two agree past 99 content rows.
	input.DynamicHeight = true
	input.MinHeight = minInputHeight
	// The real terminal cursor replaces the in-text virtual block (Phase
	// 8.2): textarea.Cursor() then reports caret coordinates the View
	// pins onto the screen.
	input.SetVirtualCursor(false)
	input.SetStyles(restyleInput(input, k).Styles())
	input.SetHeight(minInputHeight)
	input.SetWidth(startWidth - inputBorderCols)
	input.Focus() // no blink-cmd bookkeeping: the runtime blinks the real cursor

	vp := viewport.New(viewport.WithWidth(startWidth), viewport.WithHeight(startHeight-minInputHeight-inputBorderRows))
	vp.SoftWrap = true // the transcript wraps at the terminal edge (v1 behavior)

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

// Init implements tea.Model: connect, start listening, and ask the
// terminal for its background color (the theme variant selector).
func (m Model) Init() tea.Cmd {
	return tea.Batch(m.connect(), tea.RequestBackgroundColor)
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
	case tea.BackgroundColorMsg:
		return m.applyBackground(msg)
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
	case tea.KeyPressMsg:
		return m.handleKey(msg)
	case tea.KeyReleaseMsg:
		return m, nil // presses drive the app; releases are ignored
	default:
		return m, nil
	}
}

// resize recomputes layout on terminal size changes. The selection
// drops too: rewrapping moves the content lines its indices point at.
func (m *Model) resize(width, height int) {
	m.clearSelection()
	m.width, m.height = width, height
	m.vp.SetWidth(max(width-2*screenPadH, viewportMinWidth))
	setViewportHeightKeepingFollow(&m.vp, max(
		height-m.inputRows-inputBorderRows-statusRows-layoutChromeRows-headerRowsIfShown(height), viewportMinHeight,
	))
	m.input.SetWidth(max(width-2*screenPadH-inputBorderCols, viewportMinWidth))
	m.syncInputHeight()
}

// syncInputHeight grows the input box with its content — the textarea
// auto-sizes to its soft-wrapped rows (DynamicHeight, Phase 8.1) — clamps
// that to the terminal share (v2 does not know golem's layout rule), and
// returns the freed or claimed rows to the transcript viewport.
func (m *Model) syncInputHeight() {
	rows := m.input.Height()

	height := m.height
	if height == 0 {
		height = startHeight
	}

	rowCap := max(height*inputMaxShareNumerator/inputMaxShareDenominator, minInputHeight)
	rows = min(max(rows, minInputHeight), rowCap)

	// The textarea may self-size past the cap while inputRows already sits
	// at it — re-pin its height so the rendered box matches the layout.
	if rows == m.inputRows && m.input.Height() == rows {
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
	atTail := vp.AtBottom() || vp.YOffset() == 0

	vp.SetHeight(height)

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

// applyBackground flips the theme variant when the terminal reports the
// opposite background color: Tokyo Night Day on light terminals, Night
// on dark ones. Everything derived from the kit (composer styles, the
// rendered transcript) rebuilds in place.
func (m Model) applyBackground(msg tea.BackgroundColorMsg) (tea.Model, tea.Cmd) {
	if m.kit.Dark() == msg.IsDark() {
		return m, nil
	}

	m.kit = kit.NewFor(m.kit.Theme(), msg.IsDark())
	m.input = restyleInput(m.input, m.kit)
	m.refreshView()

	return m, nil
}

// restyleInput re-applies the kit tokens to the composer after a theme
// switch (the light/dark variant flip): v2 textareas carry styles in a
// Styles value rather than exported fields.
func restyleInput(input textarea.Model, k *kit.Kit) textarea.Model {
	styles := input.Styles()
	styles.Focused.Text = k.Style(kit.Fg)
	styles.Focused.Placeholder = k.Style(kit.Dim)
	styles.Blurred = styles.Focused
	// The terminal's real cursor (Phase 8.2): its color comes from the
	// kit; shape and blink stay the bubbles defaults — blinking is the
	// runtime's job, no model-side timer.
	styles.Cursor.Color = k.Color(kit.Accent)
	styles.Cursor.Shape = tea.CursorBlock
	input.SetStyles(styles)

	return input
}
