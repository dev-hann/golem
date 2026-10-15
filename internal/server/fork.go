// Session fork requests: copy a session through a turn boundary into a
// new child (ADR-0034).

package server

import (
	"errors"
	"fmt"

	"github.com/dev-hann/golem/internal/session"
	"github.com/dev-hann/golem/pkg/golemclient"
)

// handleForkSession copies the session's stored lines through a turn
// boundary into a new child session and confirms with session_forked to
// the requester only. Busy sessions are refused: a running turn's
// appends would race the copy (the same rule as compact_session).
func (s *Server) handleForkSession(writer *connWriter, ev golemclient.ForkSession) {
	if _, err := s.cfg.Store.Get(ev.Session); err != nil {
		writer.send(errorOf(ev.Session, "", "session_not_found", err))

		return
	}

	// A turn draining its terminal event is microseconds from teardown;
	// wait it out instead of rejecting the request (teardown race).
	s.waitDraining(ev.Session)

	if s.sessionBusy(ev.Session) {
		writer.send(errorOf(ev.Session, "", "invalid_request",
			fmt.Errorf("a turn is in progress for this session")))

		return
	}

	child, err := s.cfg.Store.Fork(ev.Session, ev.Turn)
	if err != nil {
		s.sendForkError(writer, ev.Session, err)

		return
	}

	writer.send(golemclient.SessionForked{
		Type:    golemclient.TypeSessionForked,
		Session: child.ID, Parent: ev.Session, Title: child.Title,
	})
}

// sendForkError maps store fork failures to wire error codes: empty
// parents and unknown turns are client errors, everything else internal.
func (s *Server) sendForkError(writer *connWriter, sessionID string, err error) {
	switch {
	case errors.Is(err, session.ErrForkEmpty), errors.Is(err, session.ErrForkTurn):
		writer.send(errorOf(sessionID, "", "invalid_request", err))
	default:
		writer.send(errorOf(sessionID, "", "internal", err))
	}
}
