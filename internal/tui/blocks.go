package tui

import (
	"encoding/json"
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
// tool blocks carry a one-line summary plus a click-to-reveal result.
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

// maxResultPreview bounds the expanded result preview so giant payloads
// cannot flood the viewport.
const maxResultPreview = 2000

// render turns one block into display lines through the kit theme. User
// messages carry the left bar in the user color; assistant output stays
// plain so the bar itself marks what the human wrote.
func (b block) render(k *kit.Kit, width int) string {
	switch b.kind {
	case blockUser:
		return k.MessageBar(kit.User, width, b.text)
	case blockAssistant:
		if b.text == "" {
			return k.DimLine("…")
		}

		return b.text
	case blockError:
		return k.ErrLine(b.text)
	case blockTool:
		return b.renderTool(k)
	default:
		return ""
	}
}

// renderTool renders a tool block: the summary line (chevron, status,
// tool, parsed argument) always, plus the result preview while expanded.
// Results hide behind a click — the transcript stays scannable.
func (b block) renderTool(k *kit.Kit) string {
	status := "…"

	if b.done {
		if b.ok {
			status = k.Marker(kit.OK, "✓")
		} else {
			status = k.Marker(kit.Error, "✗")
		}
	}

	chevron := "▸"
	if b.expanded {
		chevron = "▾"
	}

	summary := fmt.Sprintf("%s %s %s %s", chevron, status, b.tool, argSummary(b.tool, b.args))

	if !b.done || !b.expanded {
		return k.ToolLine("  " + summary)
	}

	return k.ToolLine("  "+summary) + "\n" +
		k.ToolLine("  │ "+preview(b.result, maxResultPreview))
}

// argSummary renders the one argument that identifies what a tool acted
// on: the path for file tools, the command for bash, pattern and path for
// grep. Anything else falls back to the raw JSON's first line.
func argSummary(tool, args string) string {
	var fields map[string]any
	if err := json.Unmarshal([]byte(args), &fields); err != nil {
		return firstLine(args)
	}

	switch tool {
	case "read", "edit", "write":
		return jsonField(fields, "path")
	case "bash":
		return jsonField(fields, "command")
	case "grep":
		return strings.TrimSpace(jsonField(fields, "pattern") + " " + jsonField(fields, "path"))
	case "glob":
		return jsonField(fields, "pattern")
	default:
		return firstLine(args)
	}
}

// jsonField reads one string field from decoded tool arguments.
func jsonField(fields map[string]any, key string) string {
	s, _ := fields[key].(string)

	return s
}

// firstLine trims s to its first line.
func firstLine(s string) string {
	return strings.TrimSpace(strings.SplitN(s, "\n", 2)[0])
}

// preview trims s to limit bytes with an ellipsis marker.
func preview(s string, limit int) string {
	if len(s) <= limit {
		return s
	}

	return s[:limit] + "…"
}

// userSpan locates one user block inside the rendered transcript: the
// half-open line range [start, end) and the message text. The sticky
// header uses the spans to name the question governing the visible
// region.
type userSpan struct {
	start int
	end   int
	text  string
}

// renderBlocks renders the whole transcript through the kit theme,
// separating blocks with a blank line (tool lines attach to the block
// they follow) and reporting every user and tool block's line span.
func renderBlocks(k *kit.Kit, blocks []block, width int) (out string, spans []userSpan, toolSpans []toolSpan) {
	var b strings.Builder

	line := 0

	for i, blk := range blocks {
		if i > 0 {
			// A blank line separates blocks; tool lines attach directly
			// to the block they follow. One '\n' merely ends the previous
			// line — it adds no blank row — so the line counter moves only
			// for the two-newline separator.
			if blk.kind == blockTool {
				b.WriteByte('\n')
			} else {
				b.WriteString("\n\n")

				line++
			}
		}

		rendered := blk.render(k, width)
		lines := strings.Count(rendered, "\n") + 1

		switch blk.kind {
		case blockUser:
			spans = append(spans, userSpan{start: line, end: line + lines, text: blk.text})
		case blockTool:
			toolSpans = append(toolSpans, toolSpan{start: line, end: line + lines, index: i})
		case blockAssistant, blockError:
			// No span tracking: only user and tool rows are interactive.
		}

		b.WriteString(rendered)

		line += lines
	}

	return b.String(), spans, toolSpans
}

// toolSpan locates one tool block's rendered line range and its index in
// the block list, so clicks can map back to the block they hit.
type toolSpan struct {
	start int
	end   int
	index int
}
