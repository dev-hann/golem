package server

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/dev-hann/golem/internal/llm"
	"github.com/dev-hann/golem/internal/session"
	"github.com/dev-hann/golem/pkg/golemclient"
)

// CompactSystemPrompt drives the compaction summary side call. Exported
// so test providers can recognize (and stub) the side call without
// consuming scripted turn rounds.
const CompactSystemPrompt = `You are summarizing a conversation for context compaction.
You output ONLY the summary. Nothing else.

Rules:
- Capture what was asked, what was done (files, commands, decisions), and
  the current state and next steps, in that order.
- Keep technical terms, file paths, and identifiers verbatim.
- Plain text, no markdown headings, no preamble, at most 400 words.`

// Compaction bounds (ADR-0022): the side call is time-boxed and its
// output capped so a runaway summarizer cannot stall the session or
// bloat the very context it is meant to shrink.
const (
	compactTimeout  = 60 * time.Second
	compactMaxBytes = 16 * 1024
)

// handleCompact services a compact_session event: it folds the session,
// asks the provider for a bounded summary of the conversation, and
// appends a session_compacted barrier record (ADR-0022 — storage stays
// append-only; the barrier, not a rewrite, drops prior context).
func (s *Server) handleCompact(writer *connWriter, ev golemclient.CompactSession) {
	sess, err := s.cfg.Store.Get(ev.Session)
	if err != nil {
		writer.send(errorOf(ev.Session, "", "session_not_found", err))

		return
	}

	if s.sessionBusy(ev.Session) {
		writer.send(errorOf(ev.Session, "", "invalid_request",
			errors.New("a turn is in progress for this session")))

		return
	}

	if len(sess.Messages) == 0 {
		writer.send(errorOf(ev.Session, "", "invalid_request",
			errors.New("nothing to compact: the session holds no conversation")))

		return
	}

	summary, err := s.summarizeForCompact(ev.Session, sess)
	if err != nil {
		// The session is untouched — no barrier record was appended.
		writer.send(golemclient.CompactFailed{
			Type: golemclient.TypeCompactFailed, Session: ev.Session, Reason: err.Error(),
		})

		return
	}

	record := golemclient.SessionCompacted{
		Type: golemclient.TypeSessionCompacted, Session: ev.Session, Summary: summary,
	}
	if err := s.cfg.Store.Append(ev.Session, record); err != nil {
		writer.send(golemclient.CompactFailed{
			Type: golemclient.TypeCompactFailed, Session: ev.Session, Reason: err.Error(),
		})

		return
	}

	s.cfg.Store.Invalidate(ev.Session)

	writer.send(record)
}

// summarizeForCompact runs the bounded summary side call over a
// session's folded history and returns the trimmed result.
func (s *Server) summarizeForCompact(sessionID string, sess *session.Session) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), compactTimeout)
	defer cancel()

	stream, err := s.cfg.Provider.Stream(ctx, llm.Request{
		Model:        s.turnModel(sessionID),
		SystemPrompt: CompactSystemPrompt,
		Messages:     sess.Messages,
	})
	if err != nil {
		return "", fmt.Errorf("starting summary call: %w", err)
	}

	var raw strings.Builder

	for event := range stream {
		switch event.Type {
		case llm.EventError:
			return "", fmt.Errorf("summary call failed: %w", event.Err)
		case llm.EventTextDelta:
			raw.WriteString(event.Delta)
		case llm.EventThinkingDelta, llm.EventRetry:
			// reasoning and retry noise never reach the summary
		case llm.EventToolCalls, llm.EventUsage:
			// a summary must not call tools; usage is irrelevant here
		default:
			// unknown normalized events are ignored per PROTOCOL.md
		}
	}

	if err := ctx.Err(); err != nil {
		return "", fmt.Errorf("summary call timed out: %w", err)
	}

	summary := strings.TrimSpace(raw.String())
	if summary == "" {
		return "", errors.New("summary call produced nothing")
	}

	if len(summary) > compactMaxBytes {
		summary = summary[:compactMaxBytes] + "…"
	}

	return summary, nil
}
