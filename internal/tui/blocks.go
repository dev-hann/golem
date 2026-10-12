package tui

import (
	"fmt"
	"strings"

	"github.com/charmbracelet/lipgloss"
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

// transcript styles. Colors degrade gracefully on dumb terminals.
var (
	userStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("12")).Bold(true)
	asstStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("15"))
	toolStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("8"))
	okStyle   = lipgloss.NewStyle().Foreground(lipgloss.Color("10"))
	errStyle  = lipgloss.NewStyle().Foreground(lipgloss.Color("9"))
)

// maxArgsPreview and maxResultPreview bound block sizes so giant payloads
// cannot flood the viewport.
const (
	maxArgsPreview   = 120
	maxResultPreview = 2000
)

// render turns one block into display lines.
func (b block) render() string {
	switch b.kind {
	case blockUser:
		return userStyle.Render("you> ") + b.text
	case blockAssistant:
		if b.text == "" {
			return asstStyle.Render("golem> …")
		}

		return asstStyle.Render("golem> ") + b.text
	case blockError:
		return errStyle.Render("error: ") + b.text
	case blockTool:
		return b.renderTool()
	default:
		return ""
	}
}

// renderTool renders collapsed (summary) or expanded (with result) tool
// blocks. Tool calls are collapsed by default per ROADMAP phase 3.
func (b block) renderTool() string {
	status := "…"

	if b.done {
		if b.ok {
			status = okStyle.Render("✓")
		} else {
			status = errStyle.Render("✗")
		}
	}

	summary := fmt.Sprintf("%s %s %s", status, b.tool, preview(b.args, maxArgsPreview))
	if !b.expanded || !b.done {
		return toolStyle.Render("  ") + summary
	}

	return toolStyle.Render("  ") + summary + "\n" +
		toolStyle.Render("  │ ") + preview(b.result, maxResultPreview)
}

// preview trims s to limit bytes with an ellipsis marker.
func preview(s string, limit int) string {
	if len(s) <= limit {
		return s
	}

	return s[:limit] + "…"
}

// renderBlocks renders the whole transcript.
func renderBlocks(blocks []block) string {
	lines := make([]string, 0, len(blocks))
	for _, b := range blocks {
		lines = append(lines, b.render())
	}

	return strings.Join(lines, "\n")
}
