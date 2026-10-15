// The ctrl+p command palette: registration, fuzzy filtering, and the
// palette's key handling.

package tui

import (
	"fmt"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
)

// command is one entry of the ctrl+p command palette; run executes the
// action on the model.
type command struct {
	name string
	keys string
	run  func(Model) (tea.Model, tea.Cmd)
}

// commands lists the palette entries in display order — every leader
// action plus quit, mirroring the opencode command list.
func (m Model) commands() []command {
	return []command{
		{name: "new session", keys: "ctrl+x n", run: Model.newSessionCmd},
		{name: "session list", keys: "ctrl+x l", run: Model.openSessions},
		{name: "model list", keys: "ctrl+x m", run: Model.openModels},
		{name: "compact session", keys: "/compact", run: Model.paletteCompact},
		{name: "quit", keys: "ctrl+d", run: Model.quitCmd},
	}
}

// paletteCompact runs the compaction from the command palette.
func (m Model) paletteCompact() (tea.Model, tea.Cmd) {
	return m.compactCmd()
}

// filteredCommands returns the palette commands whose names match the
// query.
func (m Model) filteredCommands() []command {
	if m.cmdQuery == "" {
		return m.commands()
	}

	var out []command

	for _, c := range m.commands() {
		if fuzzyContains(m.cmdQuery, c.name) {
			out = append(out, c)
		}
	}

	return out
}

// fuzzyContains reports whether the query's runes appear in s in order
// (case-insensitive subsequence match — "ns" hits "new session").
func fuzzyContains(query, s string) bool {
	q := []rune(strings.ToLower(query))
	i := 0

	for _, r := range strings.ToLower(s) {
		if i < len(q) && r == q[i] {
			i++
		}
	}

	return i == len(q)
}

// updateCommand drives the ctrl+p palette: typing filters, arrows (or
// the dialog keys ctrl+p/ctrl+n) move, enter runs, esc closes.
func (m Model) updateCommand(key tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch key.Type { //nolint:exhaustive // foreign enum; default ignores
	case tea.KeyEscape:
		m.mode = modeChat
		m.cmdQuery, m.cmdCursor = "", 0

		return m, nil
	case tea.KeyUp, tea.KeyCtrlP:
		m.cmdCursor = max(m.cmdCursor-1, 0)

		return m, nil
	case tea.KeyDown, tea.KeyCtrlN:
		m.cmdCursor = min(m.cmdCursor+1, len(m.filteredCommands())-1)

		return m, nil
	case tea.KeyEnter:
		cmds := m.filteredCommands()
		if len(cmds) == 0 {
			return m, nil
		}

		chosen := cmds[max(min(m.cmdCursor, len(cmds)-1), 0)]
		m.mode = modeChat
		m.cmdQuery, m.cmdCursor = "", 0

		return chosen.run(m)
	case tea.KeyBackspace:
		if runes := []rune(m.cmdQuery); len(runes) > 0 {
			m.cmdQuery = string(runes[:len(runes)-1])
			m.cmdCursor = max(min(m.cmdCursor, len(m.filteredCommands())-1), 0)
		}

		return m, nil
	case tea.KeyRunes:
		if !key.Alt {
			m.cmdQuery += string(key.Runes)
			m.cmdCursor = max(min(m.cmdCursor, len(m.filteredCommands())-1), 0)
		}

		return m, nil
	default:
		return m, nil
	}
}

// commandBody renders the palette: matching rows with their shortcuts,
// the live query, and the non-action key hints the help modal used to
// carry.
func (m Model) commandBody() string {
	var b strings.Builder

	cmds := m.filteredCommands()
	if len(cmds) == 0 {
		b.WriteString(m.kit.DimLine("no matches") + "\n")
	}

	for i, c := range cmds {
		row := fmt.Sprintf("%-13s %s", c.name, c.keys)
		fmt.Fprintf(&b, "%s\n", m.kit.Row(row, i == m.cmdCursor)) //nolint:errcheck // strings.Builder
	}

	b.WriteString("\n" + m.kit.Prompt("› "+m.cmdQuery+"_") + "\n")
	b.WriteString(m.kit.DimLine("type to filter · ↑↓ select · enter run · esc close") + "\n")
	b.WriteString(m.kit.DimLine("enter send · alt+enter/ctrl+j newline · ctrl+g top · ctrl+alt+g bottom"))

	return b.String()
}
