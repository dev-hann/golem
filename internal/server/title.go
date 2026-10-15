package server

import (
	"context"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/dev-hann/golem/internal/llm"
	"github.com/dev-hann/golem/pkg/golemclient"
)

// TitleSystemPrompt drives the session-title side call after a session's
// first turn. It is exported so test providers can recognize (and stub)
// the side call without consuming scripted turn rounds.
const TitleSystemPrompt = `You are a title generator. You output ONLY a short conversation title. Nothing else.

Rules:
- One line, at most 50 characters.
- Use the same language as the user message.
- Name the task, not the process; keep technical terms, numbers, and file names.
- Never mention tools, tests, or that you are generating a title.
- Output the title alone: no quotes, no explanation.`

// Title generation bounds. The timeout must cover the provider retry
// ladder (six backoffs, ~61s worst case — see llm.DefaultRetryPolicy);
// TestTitleTimeoutCoversRetryLadder pins that relationship.
const (
	titleTimeout  = 90 * time.Second
	titleMaxRunes = 60
)

// generateTitle asks the provider for a short title covering the session's
// first exchange and persists it as a session_title record. ADR-0032: it
// runs alongside the turn (not after turn_end), so on success the title
// is also pushed to the requesting client's connection — clients no
// longer wait for their next session_list to see it. Failures are logged
// to stderr (serve.log for spawned servers) and dropped: an untitled
// session just lists by its directory.
func (s *Server) generateTitle(writer *connWriter, sessionID, firstUserText, model string) {
	defer s.endTitleRun(sessionID)

	ctx, cancel := context.WithTimeout(context.Background(), titleTimeout)
	defer cancel()

	events, err := s.cfg.Provider.Stream(ctx, llm.Request{
		Model:        model,
		SystemPrompt: TitleSystemPrompt,
		Messages:     []llm.Message{{Role: llm.RoleUser, Text: firstUserText}},
	})
	if err != nil {
		fmt.Fprintf(os.Stderr, "golem: title generation failed: %v\n", err)

		return
	}

	var raw strings.Builder

	for ev := range events {
		if ev.Type == llm.EventError {
			fmt.Fprintf(os.Stderr, "golem: title generation failed: %v\n", ev.Err)

			return
		}

		if ev.Type == llm.EventTextDelta {
			raw.WriteString(ev.Delta)
		}
	}

	title := sanitizeTitle(raw.String())
	if title == "" {
		fmt.Fprintf(os.Stderr, "golem: title generation produced nothing (raw %q)\n", raw.String())

		return
	}

	// Append-only storage; a failed write just leaves the session
	// untitled, which the listing falls back from.
	titleEvent := golemclient.SessionTitle{
		Type: golemclient.TypeSessionTitle, Session: sessionID, Title: title,
	}

	if err := s.cfg.Store.Append(sessionID, titleEvent); err != nil {
		fmt.Fprintf(os.Stderr, "golem: title generation failed to persist: %v\n", err)

		return
	}

	writer.send(titleEvent)
}

// sanitizeTitle normalizes a generated title to one trimmed line without
// surrounding quotes, capped at titleMaxRunes with an ellipsis.
func sanitizeTitle(raw string) string {
	line := strings.TrimSpace(strings.SplitN(raw, "\n", 2)[0])
	line = strings.Trim(line, "\"'“”‘’")

	if line == "" {
		return ""
	}

	if runes := []rune(line); len(runes) > titleMaxRunes {
		line = strings.TrimRight(string(runes[:titleMaxRunes]), " -") + "…"
	}

	return line
}
