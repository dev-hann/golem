package tui

import (
	"strings"
	"testing"

	"github.com/dev-hann/golem/pkg/golemclient"
)

func TestModel_TurnEndAccumulatesUsage(t *testing.T) {
	t.Parallel()
	m, _ := wired(t)
	m = resize(m, 100, 40)
	m = deliver(t, m, golemclient.TurnStart{
		Type: golemclient.TypeTurnStart, Turn: "t1", Model: "claude-opus-5",
	})
	m = deliver(t, m, golemclient.TurnEnd{
		Type: golemclient.TypeTurnEnd, Turn: "t1",
		Usage: golemclient.Usage{InputTokens: 4200, OutputTokens: 380},
	})

	if m.usageIn != 4200 || m.usageOut != 380 {
		t.Errorf("usage = %d/%d, want 4200/380", m.usageIn, m.usageOut)
	}

	if m.lastUsage.InputTokens != 4200 {
		t.Errorf("lastUsage = %+v", m.lastUsage)
	}
}

func TestModel_UsageAccumulatesAcrossTurns(t *testing.T) {
	t.Parallel()
	m, _ := wired(t)
	m = resize(m, 100, 40)

	for _, usage := range []golemclient.Usage{
		{InputTokens: 1000, OutputTokens: 100},
		{InputTokens: 2500, OutputTokens: 200},
	} {
		m = deliver(t, m, golemclient.TurnStart{
			Type: golemclient.TypeTurnStart, Turn: "turn", Model: "m",
		})
		m = deliver(t, m, golemclient.TurnEnd{
			Type: golemclient.TypeTurnEnd, Turn: "turn", Usage: usage,
		})
	}

	if m.usageIn != 3500 || m.usageOut != 300 {
		t.Errorf("cumulative usage = %d/%d, want 3500/300", m.usageIn, m.usageOut)
	}
}

func TestModel_UsageResetsOnNewSession(t *testing.T) {
	t.Parallel()
	m, _ := wired(t)
	m = resize(m, 100, 40)
	// A session must exist before a turn can accumulate usage (lazy
	// create: the first user message owns the session).
	m = deliver(t, m, golemclient.SessionMeta{
		Type: golemclient.TypeSessionMeta, Session: "s1", Cwd: "/p",
	})
	m = deliver(t, m, golemclient.TurnEnd{
		Type: golemclient.TypeTurnEnd, Turn: "t1",
		Usage: golemclient.Usage{InputTokens: 500, OutputTokens: 50},
	})

	m2, _ := m.newSessionCmd()

	mt := m2.(Model)
	if mt.usageIn != 0 || mt.usageOut != 0 {
		t.Errorf("usage after new session = %d/%d, want 0/0", mt.usageIn, mt.usageOut)
	}
}

func TestModel_UsageRestoredFromMeta(t *testing.T) {
	t.Parallel()
	m, _ := wired(t)
	m = resize(m, 100, 40)

	// Reconnecting to a session with history: meta carries the totals.
	m = deliver(t, m, golemclient.SessionMeta{
		Type: golemclient.TypeSessionMeta, Session: m.sessionID, Cwd: "/p",
		Turns: 3, Usage: golemclient.Usage{InputTokens: 9000, OutputTokens: 900},
	})

	if m.usageIn != 9000 || m.usageOut != 900 {
		t.Errorf("usage from meta = %d/%d, want 9000/900", m.usageIn, m.usageOut)
	}
}

func TestModel_StatusLineShowsSessionTotal(t *testing.T) {
	t.Parallel()
	m, _ := wired(t)
	m = resize(m, 100, 40)
	m = deliver(t, m, golemclient.TurnStart{
		Type: golemclient.TypeTurnStart, Turn: "t1", Model: "claude-opus-5",
	})
	m = deliver(t, m, golemclient.TurnEnd{
		Type: golemclient.TypeTurnEnd, Turn: "t1",
		Usage: golemclient.Usage{InputTokens: 4200, OutputTokens: 380},
	})

	if got := m.statusLine(); !strings.Contains(got, "Σ4.6k") {
		t.Errorf("status line missing session total:\n%s", got)
	}
}

func TestSessionUsageSuffix(t *testing.T) {
	t.Parallel()

	if got := sessionUsageSuffix(0, 0); got != "" {
		t.Errorf("suffix for zero usage = %q, want empty", got)
	}

	if got := sessionUsageSuffix(900, 100); got != " · Σ1.0k" {
		t.Errorf("suffix = %q, want ' · Σ1.0k'", got)
	}
}
