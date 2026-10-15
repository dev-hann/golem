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

// DefaultAutoCompactBytes is the folded-history size that triggers an
// automatic compaction before a turn starts (ADR-0025). 128 KiB keeps
// roughly 30k tokens of context — large enough for normal work, small
// enough that reasoning-heavy models never see the runaway histories
// that burn their output budget (ADR-0023).
const DefaultAutoCompactBytes = 128 * 1024

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

	record, err := s.compactSession(context.Background(), ev.Session, sess)
	if err != nil {
		// The session is untouched — no barrier record was appended.
		writer.send(golemclient.CompactFailed{
			Type: golemclient.TypeCompactFailed, Session: ev.Session, Reason: err.Error(),
		})

		return
	}

	writer.send(record)
}

// autoCompact runs the ADR-0025 pre-turn trigger: when the folded
// history measures at or above the configured threshold, compact it
// through the same barrier path manual /compact uses. The turn always
// proceeds: a failed compaction emits compact_failed and the turn runs
// on the un-compacted history (compaction is an optimization, never a
// gate). The caller invokes this before appending the pending user
// message, so that message lands after any barrier and always survives.
func (s *Server) autoCompact(ctx context.Context, writer *connWriter, sessionID string) {
	if s.cfg.AutoCompactBytes <= 0 {
		return
	}

	// The summary side call must die with the turn: an interrupt (or
	// delete_session's cancel) cannot leave it streaming in the
	// background.
	if ctx.Err() != nil {
		return
	}

	sess, err := s.cfg.Store.Get(sessionID)
	if err != nil {
		// A load failure cannot compact, but must not kill the turn;
		// runTurn re-reads the session and reports load errors there.
		return
	}

	if sess.ContextBytes() < s.cfg.AutoCompactBytes {
		return
	}

	record, err := s.compactSession(ctx, sessionID, sess)
	if err != nil {
		writer.send(golemclient.CompactFailed{
			Type: golemclient.TypeCompactFailed, Session: sessionID,
			Reason: "auto compaction skipped: " + err.Error(),
		})

		return
	}

	writer.send(record)
}

// compactSession summarizes a session's folded history and appends the
// session_compacted barrier record (the shared body of the manual and
// automatic paths, ADR-0022/ADR-0025). ctx bounds the side call — for the
// automatic path it is the turn context, so an interrupt cancels an
// in-flight summary; the manual path passes Background. The cache is
// invalidated so the next Get folds from disk.
func (s *Server) compactSession(
	ctx context.Context,
	sessionID string,
	sess *session.Session,
) (golemclient.SessionCompacted, error) {
	summary, err := s.summarizeForCompact(ctx, sessionID, sess)
	if err != nil {
		return golemclient.SessionCompacted{}, err
	}

	record := golemclient.SessionCompacted{
		Type: golemclient.TypeSessionCompacted, Session: sessionID, Summary: summary,
	}
	if err := s.cfg.Store.Append(sessionID, record); err != nil {
		return golemclient.SessionCompacted{}, err
	}

	return record, nil
}

// summarizeForCompact runs the bounded summary side call over a
// session's folded history and returns the trimmed result. The deadline
// derives from the caller's context: the automatic path passes the turn
// context (an interrupt kills the summary with the turn, ADR-0025), the
// manual path passes Background with the compactTimeout cap.
func (s *Server) summarizeForCompact(
	ctx context.Context,
	sessionID string,
	sess *session.Session,
) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, compactTimeout)
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
