package tui

import (
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"

	"github.com/dev-hann/golem/pkg/golemclient"
)

// TestRender_SessionPickerQueryLine documents the picker's search-field
// rendering across modes: the query prompt is always visible, and the
// rename input replaces the selected row (not the query line).
func TestRender_SessionPickerQueryLine(t *testing.T) {
	t.Parallel()

	m, fc := wired(t)
	m = resize(m, 100, 40)
	m.listRequest = true
	m = deliver(t, m, golemclient.SessionList{
		Type: golemclient.TypeSessionList,
		Sessions: []golemclient.SessionSummary{
			{ID: "s1", Title: "Fix auth middleware", Cwd: "/a"},
			{ID: "s2", Title: "Add session tests", Cwd: "/b"},
		},
	})

	for _, r := range "tes" {
		m = key(t, m, rkey(r))
	}

	if got := m.View().Content; !strings.Contains(got, "› tes_") {
		t.Errorf("search mode must render the query:\n%s", got)
	}

	m = key(t, m, tea.KeyPressMsg{Code: 'r', Mod: tea.ModCtrl})
	if got := m.View().Content; !strings.Contains(got, "› Add session tests_") {
		t.Errorf("rename mode must render the rename input on the selected row:\n%s", got)
	}

	_ = fc
}
