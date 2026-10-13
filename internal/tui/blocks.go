package tui

import (
	"fmt"
	"strings"

	"github.com/dev-hann/golem/internal/tui/kit"
)

// blockKind distinguishes transcript entries.
type blockKind int

// Transcript block kinds.
const (
	blockUser blockKind = iota
	blockAssistant
	blockTool
	blockError
)

// block is one transcript entry. Assistant blocks accumulate streamed text;
// tool blocks carry a collapsed summary and an expandable result.
type block struct {
	kind     blockKind
	text     string
	callID   string
	tool     string
	args     string
	result   string
	ok       bool
	done     bool
	expanded bool
}

// maxArgsPreview and maxResultPreview bound block sizes so giant payloads
// cannot flood the viewport.
const (
	maxArgsPreview   = 120
	maxResultPreview = 2000
)

// render turns one block into display lines through the kit theme. Both
// chat sides carry a left bar in their role color; the assistant token
// sits too close to the default foreground, so assistant bars borrow the
// accent color to stay distinguishable.
func (b block) render(k *kit.Kit, width int) string {
	switch b.kind {
	case blockUser:
		return k.MessageBar(kit.User, width, b.text)
	case blockAssistant:
		if b.text == "" {
			return k.MessageBar(kit.Accent, width, "…")
		}

		return k.MessageBar(kit.Accent, width, b.text)
	case blockError:
		return k.ErrLine(b.text)
	case blockTool:
		return b.renderTool(k)
	default:
		return ""
	}
}

// renderTool renders collapsed (summary) or expanded (with result) tool
// blocks. Tool calls are collapsed by default per ROADMAP phase 3.
func (b block) renderTool(k *kit.Kit) string {
	status := "…"

	if b.done {
		if b.ok {
			status = k.Marker(kit.OK, "✓")
		} else {
			status = k.Marker(kit.Error, "✗")
		}
	}

	summary := fmt.Sprintf("%s %s %s", status, b.tool, preview(b.args, maxArgsPreview))
	if !b.expanded || !b.done {
		return k.ToolLine("  " + summary)
	}

	return k.ToolLine("  "+summary) + "\n" +
		k.ToolLine("  │ "+preview(b.result, maxResultPreview))
}

// preview trims s to limit bytes with an ellipsis marker.
func preview(s string, limit int) string {
	if len(s) <= limit {
		return s
	}

	return s[:limit] + "…"
}

// renderBlocks renders the whole transcript through the kit theme,
// separating blocks with a blank line (tool lines attach to the block
// they follow). It also reports lastUserEnd: the line index one past the
// final user block in the joined output, or -1 with no user block — the
// scroll position the sticky header keys off.
func renderBlocks(k *kit.Kit, blocks []block, width int) (out string, lastUserEnd int) {
	var b strings.Builder

	lastUserEnd = -1
	line := 0

	for i, blk := range blocks {
		if i > 0 {
			// A blank line separates blocks; tool lines attach directly
			// to the block they follow with a single newline.
			if blk.kind == blockTool {
				b.WriteByte('\n')

				line++
			} else {
				b.WriteString("\n\n")

				line += 2
			}
		}

		rendered := blk.render(k, width)
		lines := strings.Count(rendered, "\n") + 1

		if blk.kind == blockUser {
			lastUserEnd = line + lines
		}

		b.WriteString(rendered)

		line += lines
	}

	return b.String(), lastUserEnd
}
