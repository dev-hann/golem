// Scrollback navigation: wheel, paging, and drag-to-click mapping over
// the transcript viewport.

package tui

import (
	tea "github.com/charmbracelet/bubbletea"
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
func (m *Model) scrollKey(key tea.KeyMsg) bool {
	switch key.Type { //nolint:exhaustive // only the scroll keys are ours
	case tea.KeyPgUp:
		m.scrollBy(-pageLines)

		return true
	case tea.KeyPgDown:
		m.scrollBy(pageLines)

		return true
	case tea.KeyCtrlG:
		if key.Alt {
			m.vp.GotoBottom()
			m.follow = true
		} else {
			m.vp.GotoTop()
			m.follow = false
		}

		return true
	default:
		return false
	}
}

// mouseClick toggles the tool block under a left click — the chevron
// rows — reporting whether one was hit. Clicks on the composer, the
// sticky header, or modal backdrops are not tool rows and do nothing.
func (m *Model) mouseClick(msg tea.MouseMsg) bool {
	if m.mode != modeChat {
		return false
	}

	line, ok := m.lineAt(msg.Y)
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
	switch {
	case msg.Button == tea.MouseButtonWheelUp:
		if m.mode == modeSessions {
			m.scrollPicker(-wheelLines)

			return
		}

		m.scrollBy(-wheelLines)
	case msg.Button == tea.MouseButtonWheelDown:
		if m.mode == modeSessions {
			m.scrollPicker(wheelLines)

			return
		}

		m.scrollBy(wheelLines)
	case msg.Button == tea.MouseButtonLeft && msg.Action == tea.MouseActionPress:
		if !m.mouseClick(msg) {
			m.beginSelection(msg.Y)
		}
	case msg.Button == tea.MouseButtonLeft && msg.Action == tea.MouseActionMotion:
		m.extendSelection(msg.Y)
	case msg.Action == tea.MouseActionRelease &&
		(msg.Button == tea.MouseButtonLeft || msg.Button == tea.MouseButtonNone):
		m.endSelection()
	default:
	}
}
