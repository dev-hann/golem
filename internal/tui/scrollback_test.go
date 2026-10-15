package tui

import (
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/dev-hann/golem/pkg/golemclient"
)

// The scrollback bug (2026-10-15 report): loading a past session from
// the picker, the transcript could not be scrolled to its beginning.
// Root cause: foldReplayEvent applied the compaction barrier's context
// rule to the *view*, dropping every transcript block before each
// session_compacted record. Auto-compaction (ADR-0025) fires that
// barrier regularly on long sessions, so most old sessions rendered as
// just the tail after the last summary — the beginning was unreachable
// no matter how far the user scrolled.
//
// This test pins the fix: replay keeps pre-barrier blocks in the view;
// the dim barrier block still renders where it happened.

func TestReplayHistoryKeepsBlocksBeforeCompactionBarrier(t *testing.T) {
	t.Parallel()

	m, _ := wired(t)
	m = resize(m, 100, 30)

	m.replayHistory(golemclient.History{
		Type:    golemclient.TypeHistory,
		Session: "s1",
		Events: mustJSON(t,
			userLine("first question ever"),
			deltaLine("the first answer"),
			compactedLine("SUMMARY ONE"),
			userLine("second question"),
			deltaLine("second answer"),
			compactedLine("SUMMARY TWO"),
			userLine("third question"),
			deltaLine("the tail"),
		),
	})

	// Both barriers render, in order, with their summaries.
	var (
		sawOne, sawTwo     bool
		sawFirst, sawThird bool
	)
	for _, b := range m.blocks {
		switch {
		case b.kind == blockDim && strings.Contains(b.text, "SUMMARY ONE"):
			sawOne = true
		case b.kind == blockDim && strings.Contains(b.text, "SUMMARY TWO"):
			sawTwo = true
		case b.kind == blockUser && b.text == "first question ever":
			sawFirst = true
		case b.kind == blockUser && b.text == "third question":
			sawThird = true
		}
	}

	if !sawOne || !sawTwo {
		t.Errorf("barrier summaries must render: one=%v two=%v blocks=%+v", sawOne, sawTwo, m.blocks)
	}
	if !sawFirst {
		t.Errorf("pre-barrier transcript must survive replay for the view: blocks=%+v", m.blocks)
	}
	if !sawThird {
		t.Errorf("post-barrier transcript must render: blocks=%+v", m.blocks)
	}

	// The whole session is scrollable: the first question is reachable
	// by scrolling up from the tail (the load leaves the view at bottom).
	if m.vp.AtBottom() {
		for range 50 {
			m = key(t, m, tea.KeyMsg{Type: tea.KeyPgUp})
		}
	}

	if !strings.Contains(m.View(), "first question ever") {
		t.Errorf("scrolling up must reach the session's beginning:\n%s", m.View())
	}
}

// The live path must match the replay path: a compaction confirmation
// arriving mid-session keeps the earlier transcript on screen.
func TestLiveCompactionKeepsEarlierTranscript(t *testing.T) {
	t.Parallel()

	m, _ := wired(t)
	m = resize(m, 100, 30)
	m.sessionID = "s1"

	m.blocks = []block{
		{kind: blockUser, text: "old question"},
		{kind: blockAssistant, text: "old answer"},
	}
	m.refreshView()

	m.applyEvent(golemclient.SessionCompacted{
		Type: golemclient.TypeSessionCompacted, Session: "s1", Summary: "LIVE SUMMARY",
	})

	var sawOld, sawSummary bool

	for _, b := range m.blocks {
		switch {
		case b.kind == blockUser && b.text == "old question":
			sawOld = true
		case b.kind == blockDim && strings.Contains(b.text, "LIVE SUMMARY"):
			sawSummary = true
		}
	}

	if !sawOld {
		t.Errorf("live compaction must not drop earlier view blocks: %+v", m.blocks)
	}
	if !sawSummary {
		t.Errorf("live compaction must append its summary block: %+v", m.blocks)
	}
}
