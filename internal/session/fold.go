package session

import (
	"encoding/json"

	"github.com/dev-hann/golem/internal/llm"
)

// folder incrementally rebuilds conversation state from stored wire events.
// See docs/PROTOCOL.md: storage lines are the wire events themselves.
type folder struct {
	messages []llm.Message
	turns    int

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
		var ev struct {
			Text string `json:"text"`
		}
		if err := json.Unmarshal(line, &ev); err != nil || ev.Text == "" {
			return
		}

		f.turnUser = &llm.Message{Role: llm.RoleUser, Text: ev.Text}
	case "text_delta":
		var ev struct {
			Delta string `json:"delta"`
		}
		if err := json.Unmarshal(line, &ev); err != nil {
			return
		}

		f.asstText += ev.Delta
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
	default:
		// session_meta / approval / error / interrupt events do not change
		// conversation state.
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
