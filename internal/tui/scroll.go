// Scrollback navigation: wheel, paging, and drag-to-click mapping over
// the transcript viewport.

package tui

import (
	tea "charm.land/bubbletea/v2"
)

// pageLines is the scroll step for page keys.
const pageLines = 10

// scrollBy scrolls the transcript by delta lines (negative scrolls back
// toward older output). Manual scrollback suspends auto-follow; reaching
// the bottom resumes it.
func (m *Model) scrollBy(delta int) {
	if delta < 0 {
		m.follow = false
		m.vp.ScrollUp(-delta)

		return
	}

	m.vp.ScrollDown(delta)

	if m.vp.AtBottom() {
		m.follow = true
	}
}

// wheelLines is the scroll step for one mouse-wheel notch.
const wheelLines = 3

// scrollKey handles transcript-scroll keys, reporting whether it
// consumed the key. ctrl+g / ctrl+alt+g jump to the top / bottom; plain
// home / end stay with the composer, matching opencode's split.
func (m *Model) scrollKey(key tea.KeyPressMsg) bool {
	switch key.String() { //nolint:exhaustive // only the scroll keys are ours
	case "pgup":
		m.scrollBy(-pageLines)

		return true
	case "pgdown":
		m.scrollBy(pageLines)

		return true
	case "ctrl+alt+g":
		m.vp.GotoBottom()
		m.follow = true

		return true
	case "ctrl+g":
		m.vp.GotoTop()
		m.follow = false

		return true
	default:
		return false
	}
}

// mouseClick toggles the tool block under a left click — the chevron
// rows — reporting whether one was hit. Clicks on the composer, the
// sticky header, or modal backdrops are not tool rows and do nothing.
func (m *Model) mouseClick(_, y int) bool {
	if m.mode != modeChat {
		return false
	}

	line, ok := m.lineAt(y)
	if !ok {
		return false
	}

	for _, span := range m.toolSpans {
		if line >= span.start && line < span.end {
			m.blocks[span.index].expanded = !m.blocks[span.index].expanded
			m.refreshView()

			return true
		}
	}

	return false
}

// handleMouse routes mouse input: the wheel scrolls whatever modal is
// open (the session picker's list) or the transcript, a left press
// toggles the tool row under it or begins a text selection, drags
// extend the selection, and the release finishes it.
func (m *Model) handleMouse(msg tea.MouseMsg) {
	switch msg := msg.(type) {
	case tea.MouseWheelMsg:
		delta := -wheelLines // wheel up scrolls toward older output
		if msg.Button != tea.MouseWheelUp {
			delta = wheelLines
		}

		if m.mode == modeSessions {
			m.scrollPicker(delta)

			return
		}

		m.scrollBy(delta)
	case tea.MouseClickMsg:
		if msg.Button == tea.MouseLeft && !m.mouseClick(msg.X, msg.Y) {
			m.beginSelection(msg.Y)
		}
	case tea.MouseMotionMsg:
		if msg.Button == tea.MouseLeft {
			m.extendSelection(msg.Y)
		}
	case tea.MouseReleaseMsg:
		if msg.Button == tea.MouseLeft || msg.Button == 0 {
			m.endSelection()
		}
	default:
	}
}
