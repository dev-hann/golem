// Bubble Tea runtime plumbing: connection lifecycle messages, the
// shared animation ticker, and the leader/exit timeout windows. Every
// timed message carries the generation it was armed for so a stale tick
// from a disarmed sequence cannot act (the leader-timeout gen pattern).

package tui

import (
	"time"

	tea "github.com/charmbracelet/bubbletea"
)

// Messages crossing into the Elm loop.
type (
	connectedMsg   struct{ conn client }
	connErrMsg     struct{ err error }
	serverEventMsg struct{ ev any }
)

// leaderTimeoutMsg disarms a leader sequence whose window elapsed; gen
// identifies which arm it belongs to so a stale tick cannot disarm a
// newer sequence.
type leaderTimeoutMsg struct{ gen int }

// spinTickMsg advances an animation frame (the picker's spinner rows,
// the status strip's in-progress glyph); gen guards against a stale
// tick from a disarmed animation.
type spinTickMsg struct{ gen int }

// spinTickInterval is the animation rate — fast enough to read as
// motion, slow enough not to churn redraws.
const spinTickInterval = 100 * time.Millisecond

// armSpinTick schedules the next animation frame.
func armSpinTick(gen int) tea.Cmd {
	return tea.Tick(spinTickInterval, func(time.Time) tea.Msg {
		return spinTickMsg{gen: gen}
	})
}

// spinTick advances the animation frame: an open picker animates its
// rows, an active turn animates the status strip. Ticks from a closed
// picker with no live turn (or a stale generation) are dropped.
func (m Model) spinTick(msg spinTickMsg) (tea.Model, tea.Cmd) {
	if m.spinGen != msg.gen {
		return m, nil
	}

	if m.mode == modeSessions || m.turnActive {
		m.spinFrame++

		return m, armSpinTick(m.spinGen)
	}

	return m, nil
}

// exitTimeoutMsg disarms an exit confirmation whose window elapsed; gen
// identifies which arm it belongs to so a stale tick cannot disarm a
// newer confirmation.
type exitTimeoutMsg struct{ gen int }

// leaderTimeout bounds how long a ctrl+x sequence waits for its key.
const leaderTimeout = 2 * time.Second

// armLeader starts the leader timeout window for gen.
func armLeader(gen int) tea.Cmd {
	return tea.Tick(leaderTimeout, func(time.Time) tea.Msg {
		return leaderTimeoutMsg{gen: gen}
	})
}

// timeoutLeader disarms a leader sequence whose window elapsed; a stale
// generation (a newer arm) leaves the mode alone.
func (m Model) timeoutLeader(msg leaderTimeoutMsg) (tea.Model, tea.Cmd) {
	if m.leaderArmed && m.leaderGen == msg.gen {
		m.setLeader(false)
	}

	return m, nil
}

// timeoutExit disarms an exit confirmation whose window elapsed; a
// stale generation (a newer arm) leaves the hint alone.
func (m Model) timeoutExit(msg exitTimeoutMsg) (tea.Model, tea.Cmd) {
	if m.exitArmed && m.exitGen == msg.gen {
		m.setExit(false)
	}

	return m, nil
}
