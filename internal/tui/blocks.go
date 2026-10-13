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

// render turns one block into display lines through the kit theme.
func (b block) render(k *kit.Kit) string {
	switch b.kind {
	case blockUser:
		return k.User("you> ", b.text)
	case blockAssistant:
		if b.text == "" {
			return k.Assistant("golem> ", "…")
		}

		return k.Assistant("golem> ", b.text)
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

// renderBlocks renders the whole transcript through the kit theme.
func renderBlocks(k *kit.Kit, blocks []block) string {
	lines := make([]string, 0, len(blocks))
	for _, b := range blocks {
		lines = append(lines, b.render(k))
	}

	return strings.Join(lines, "\n")
}
