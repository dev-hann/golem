package session

import (
	"encoding/json"

	"github.com/dev-hann/golem/internal/llm"
)

// folder incrementally rebuilds conversation state from stored wire events.
// See docs/PROTOCOL.md: storage lines are the wire events themselves.
type folder struct {
	messages      []llm.Message
	turns         int
	title         string
	firstUserText string

	// sawMeta records that the session_meta header line was folded;
	// cwd carries what it said (first wins — the file may grow stray
	// meta lines; the header Create writes is authoritative).
	sawMeta bool
	cwd     string

	// compacted flags a session_compacted barrier seen while folding:
	// everything before it dropped from context, replaced by the
	// summary message (ADR-0022).
	compacted bool

	// per-turn accumulation
	turnUser    *llm.Message
	asstText    string
	asstCalls   []llm.ToolCall
	toolResults []llm.ToolResult
}

// newFolder builds an empty folder.
func newFolder() *folder {
	return &folder{}
}

// feed folds one raw JSON line. Unknown event types are skipped per
// PROTOCOL.md forward-compatibility rules.
func (f *folder) feed(line []byte) {
	var env struct {
		Type string `json:"type"`
	}
	if err := json.Unmarshal(line, &env); err != nil {
		return // malformed lines are skipped, not fatal
	}

	switch env.Type {
	case "user_message":
		f.feedUserMessage(line)
	case "text_delta":
		f.feedTextDelta(line)
	case "tool_call_start":
		var ev struct {
			CallID string          `json:"call_id"`
			Tool   string          `json:"tool"`
			Args   json.RawMessage `json:"args"`
		}
		if err := json.Unmarshal(line, &ev); err != nil {
			return
		}

		f.asstCalls = append(f.asstCalls,
			llm.ToolCall{ID: ev.CallID, Name: ev.Tool, Args: ev.Args})
	case "tool_call_end":
		var ev struct {
			CallID string `json:"call_id"`
			OK     bool   `json:"ok"`
			Result string `json:"result"`
		}
		if err := json.Unmarshal(line, &ev); err != nil {
			return
		}

		f.toolResults = append(f.toolResults, llm.ToolResult{
			CallID: ev.CallID, OK: ev.OK, Output: ev.Result,
		})
	case "turn_end":
		f.closeTurn(true)
	case "session_title":
		var ev struct {
			Title string `json:"title"`
		}
		if err := json.Unmarshal(line, &ev); err != nil || ev.Title == "" {
			return
		}

		f.title = ev.Title
	case "session_compacted":
		f.feedCompacted(line)
	case "session_meta":
		f.feedSessionMeta(line)
	default:
		// error / interrupt events do not change conversation state.
	}
}

// feedSessionMeta folds the self-describing header line: the first
// session_meta records the session's cwd. This keeps loading to one pass
// over the file.
func (f *folder) feedSessionMeta(line []byte) {
	if f.sawMeta {
		return
	}

	f.sawMeta = true

	var ev struct {
		Cwd string `json:"cwd"`
	}
	if err := json.Unmarshal(line, &ev); err != nil {
		return
	}

	f.cwd = ev.Cwd
}

// feedTextDelta folds one text_delta line into the assistant buffer.
func (f *folder) feedTextDelta(line []byte) {
	var ev struct {
		Delta string `json:"delta"`
	}
	if err := json.Unmarshal(line, &ev); err != nil {
		return
	}

	f.asstText += ev.Delta
}

// feedCompacted decodes one session_compacted line and applies it.
func (f *folder) feedCompacted(line []byte) {
	var ev struct {
		Summary string `json:"summary"`
	}
	if err := json.Unmarshal(line, &ev); err != nil || ev.Summary == "" {
		return
	}

	f.applyCompacted(ev.Summary)
}

// applyCompacted applies a compaction barrier (ADR-0022): everything
// folded so far drops from the model's context, replaced by one
// synthesized user message carrying the summary. Title and the turn
// count are session facts, not context — both survive. FirstUserText
// resets so a pending title side call cannot resurrect the compacted
// opener.
func (f *folder) applyCompacted(summary string) {
	f.closeTurn(false)

	f.messages = []llm.Message{{
		Role: llm.RoleUser,
		Text: contextSummaryPrefix + summary,
	}}
	f.compacted = true
	f.firstUserText = ""
}

// contextSummaryPrefix labels the synthesized message a compaction
// barrier leaves in place of prior history.
const contextSummaryPrefix = "[Context summary]\n"

// feedUserMessage folds one user_message line: it opens the current turn
// and, as the session's opening message, names the task for title
// generation.
func (f *folder) feedUserMessage(line []byte) {
	var ev struct {
		Text string `json:"text"`
	}
	if err := json.Unmarshal(line, &ev); err != nil || ev.Text == "" {
		return
	}

	f.turnUser = &llm.Message{Role: llm.RoleUser, Text: ev.Text}

	if f.firstUserText == "" {
		f.firstUserText = ev.Text
	}
}

// messages returns folded history, flushing any trailing partial turn.
func (f *folder) history() []llm.Message {
	f.closeTurn(false)
	return f.messages
}

// closeTurn seals the current turn. counted reports whether it ended with a
// turn_end event (crash-truncated turns still contribute their messages but
// do not increment the turn count).
func (f *folder) closeTurn(counted bool) {
	if f.turnUser != nil {
		f.messages = append(f.messages, *f.turnUser)
		f.turnUser = nil
	}

	if f.asstText != "" || len(f.asstCalls) > 0 {
		f.messages = append(f.messages, llm.Message{
			Role:      llm.RoleAssistant,
			Text:      f.asstText,
			ToolCalls: f.asstCalls,
		})
		f.asstText, f.asstCalls = "", nil
	}

	if len(f.toolResults) > 0 {
		f.messages = append(f.messages, llm.Message{
			Role:        llm.RoleUser,
			ToolResults: f.toolResults,
		})
		f.toolResults = nil
	}

	if counted {
		f.turns++
	}
}
