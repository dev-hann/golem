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

// Title generation bounds.
const (
	titleTimeout  = 15 * time.Second
	titleMaxRunes = 60
)

// generateTitle asks the provider for a short title covering the session's
// first exchange and persists it as a session_title record. Failures are
// logged to stderr (serve.log for spawned servers) and dropped: an
// untitled session just lists by its directory.
func (s *Server) generateTitle(sessionID, firstUserText, model string) {
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
	_ = s.cfg.Store.Append(sessionID, golemclient.SessionTitle{
		Type: golemclient.TypeSessionTitle, Session: sessionID, Title: title,
	})
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
