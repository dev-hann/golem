// Server-event folding: streaming events and history replay mutate the
// model here (apply*, replay*, refreshView). The storage fold lives in
// internal/session; this is the view-side fold over the same wire
// events.

package tui

import (
	tea "charm.land/bubbletea/v2"

	"github.com/dev-hann/golem/pkg/golemclient"
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
	case golemclient.SessionForked:
		cmd = m.applySessionForked(e)
	case golemclient.SessionList:
		tick = m.applySessionList(e)
	case golemclient.History:
		m.replayHistory(e)
	case golemclient.TurnStart:
		m.applyTurnEvent(ev)
		m.applySessionRecord(ev)

		if m.turnActive { // arm the status strip's animation
			tick = armSpinTick(m.spinGen)
		}
	default:
		// Turn events fold through applyTurnEvent; session records
		// (title, deletion, compaction) through applySessionRecord;
		// anything else is ignored per PROTOCOL.md forward
		// compatibility.
		m.applyTurnEvent(ev)
		m.applySessionRecord(ev)
	}

	// A busy session resumed mid-turn (replayHistory set turnActive
	// above) arms the animation too — the strip must not freeze.
	if m.turnActive && tick == nil {
		m.spinFrame = 0
		m.spinGen++
		tick = armSpinTick(m.spinGen)
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
// starting on the session this client is in. Opening arms the animation
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
			m.spinFrame = 0
			m.spinGen++

			return armSpinTick(m.spinGen)
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
		m.spinFrame = 0
		m.spinGen++ // orphan any open animation; this one is live
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
	case golemclient.SessionForked:
		m.applySessionForked(e)
	case golemclient.SessionCompacted:
		m.applyCompacted(e)
	case golemclient.CompactFailed:
		m.applyCompactFailed(e)
	default:
		// not a session record; nothing to fold
	}
}

// applySessionForked folds the fork confirmation: the client switches
// to the child session — its transcript clears and loads the child's
// history, exactly like a picker open (ADR-0034). The dim fork notice
// lands first so the fresh transcript explains why it is a copy.
func (m *Model) applySessionForked(e golemclient.SessionForked) tea.Cmd {
	m.sessionID = e.Session
	m.sessionTitle = e.Title
	m.clearTranscript()

	m.blocks = append(m.blocks, block{
		kind: blockDim,
		text: "⑂ forked — a copy of the parent session through its last turn",
	})

	// The load runs inline, not as a cmd: the switch must precede any
	// message the user types next on the wire (flushPending's rule —
	// tea.Batch executes concurrently). A send failure surfaces as
	// connErrMsg.
	if m.client != nil {
		if err := m.client.Send(golemclient.LoadSession{
			Type: golemclient.TypeLoadSession, Session: e.Session,
		}); err != nil {
			return func() tea.Msg { return connErrMsg{err: err} }
		}
	}

	return nil
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
// compaction barrier (ADR-0022) renders as a dim summary block in the
// view — the transcript keeps the turns before it, because the view is
// the audit surface, not the model's context; the server's fold rule
// (drop everything before the barrier) applies to context only.
func (m *Model) foldReplayEvent(ev any, live replayTurn) replayTurn {
	switch e := ev.(type) {
	case golemclient.SessionCompacted:
		m.blocks = append(m.blocks, blockDimCompacted(e))
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
	content, spans, toolSpans := renderBlocks(m.kit, m.md, m.blocks, m.vp.Width())
	m.userSpans = spans
	m.toolSpans = toolSpans
	m.content = content
	m.vp.SetContent(m.highlight(content))

	if m.turnActive && m.follow {
		m.vp.GotoBottom()
	}
}
