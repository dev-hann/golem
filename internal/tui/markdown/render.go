package markdown

import (
	"fmt"
	"strings"

	"github.com/charmbracelet/x/ansi"

	"github.com/dev-hann/golem/internal/tui/kit"
)

// Render renders markdown text as styled terminal lines through the kit
// palette. Width is the hard wrap cap; blocks join with blank lines.
func Render(k *kit.Kit, width int, text string) string {
	if strings.TrimSpace(text) == "" {
		return text
	}

	blocks := Parse(text)

	var b strings.Builder

	for i, blk := range blocks {
		if i > 0 {
			b.WriteString("\n\n")
		}

		b.WriteString(renderBlock(k, width, blk))
	}

	return strings.TrimSuffix(b.String(), "\n")
}

// renderBlock renders one block through the kit.
func renderBlock(k *kit.Kit, width int, blk Block) string {
	switch blk.Kind {
	case BlockHeading:
		return k.Title(renderInline(k, width, blk.Text))
	case BlockParagraph:
		return renderInline(k, width, blk.Text)
	case BlockCode:
		return renderCode(k, width, blk)
	case BlockList:
		return renderList(k, width, blk)
	case BlockQuote:
		return renderQuote(k, width, blk)
	case BlockRule:
		return k.Divider(min(width, dividerCap))
	case BlockTable:
		return renderTable(k, blk)
	default:
		return k.WrapPlain(width, blk.Text)
	}
}

// dividerCap bounds a rule's width so short terminals keep proportion.
const dividerCap = 40

// renderInline renders one text run with inline markers, wrapped.
func renderInline(k *kit.Kit, width int, text string) string {
	styled := styleSpans(k, Inline(text))
	return k.WrapPlain(width, styled)
}

// styleSpans styles each span through the kit and concatenates.
func styleSpans(k *kit.Kit, spans []Span) string {
	var b strings.Builder
	for _, s := range spans {
		b.WriteString(styleSpan(k, s))
	}

	return b.String()
}

// styleSpan styles one span with its token and weight.
func styleSpan(k *kit.Kit, s Span) string {
	switch {
	case s.Code:
		return k.Style(kit.Assistant).Render(s.Text)
	case s.Link:
		return k.Style(kit.Accent).Render(s.Text)
	case s.Dim:
		return k.Style(kit.Dim).Render(s.Text)
	case s.Bold:
		return k.Style(kit.Fg).Bold(true).Render(s.Text)
	case s.Italic:
		return k.Style(kit.Fg).Italic(true).Render(s.Text)
	default:
		return s.Text
	}
}

// codeGeometry: the code block's gutter and the label's leading space.
const (
	codeGutter  = "│ "
	codeGutterW = 2
)

// renderCode frames a code block: dim language label row, then body
// rows behind a left bar, never wrapping (code wraps in the source, not
// the render). The caller's width only bounds nothing here — code rows
// pass through verbatim per the no-wrap rule.
func renderCode(k *kit.Kit, _ int, blk Block) string {
	var b strings.Builder

	if blk.Lang != "" {
		b.WriteString(k.DimLine(blk.Lang))
		b.WriteString("\n")
	}

	body := strings.Split(strings.TrimSuffix(blk.Text, "\n"), "\n")

	for _, line := range body {
		b.WriteString(k.Style(kit.Border).Render(codeGutter) + line)
		b.WriteString("\n")
	}

	return strings.TrimSuffix(b.String(), "\n")
}

// renderList renders list items with markers and hanging indents.
func renderList(k *kit.Kit, width int, blk Block) string {
	var rows []string

	for i, item := range blk.Items {
		mark := "• "
		if blk.Ordered {
			mark = fmt.Sprintf("%d. ", i+1)
		}

		text := styleSpans(k, Inline(item))
		inner := max(width-ansi.StringWidth(mark), 1)

		wrapped := strings.Split(k.WrapPlain(inner, text), "\n")

		for j, row := range wrapped {
			if j == 0 {
				rows = append(rows, k.Marker(kit.Accent, mark)+row)
				continue
			}

			rows = append(rows, strings.Repeat(" ", ansi.StringWidth(mark))+row)
		}
	}

	return strings.Join(rows, "\n")
}

// renderQuote renders a quote block: dim text behind a left bar, each
// quoted line its own row.
func renderQuote(k *kit.Kit, width int, blk Block) string {
	bar := k.Style(kit.Dim).Render("▎")

	var rows []string

	for _, line := range strings.Split(blk.Text, "\n") {
		text := styleSpans(k, Inline(line))
		inner := max(width-2, 1)

		for _, row := range strings.Split(k.WrapPlain(inner, text), "\n") {
			rows = append(rows, bar+" "+row)
		}
	}

	return strings.Join(rows, "\n")
}

// Table geometry: the separator row's dash rune and minimum cell width.
const (
	tableSep    = "─"
	tableMinCol = 1
)

// renderTable renders a table as a pipe grid (ADR-0027): the header
// row bold, a border-colored separator of ─ runs, body rows padded to
// each column's measured width. Alignment shifts padding left/right;
// cells never wrap (a widened row would misalign the grid).
func renderTable(k *kit.Kit, blk Block) string {
	if len(blk.Cells) == 0 {
		return ""
	}

	widths := columnWidths(k, blk)

	var rows []string

	for r, row := range blk.Cells {
		rows = append(rows, tableRow(k, blk.Align, widths, row, r == 0))

		if r == 0 {
			rows = append(rows, tableSeparator(k, widths))
		}
	}

	return strings.Join(rows, "\n")
}

// columnWidths measures each column's styled width — markers drop
// before rendering, so the grid must size against the styled text: the
// max over rows, floored at one cell.
func columnWidths(k *kit.Kit, blk Block) []int {
	widths := make([]int, len(blk.Align))

	for _, row := range blk.Cells {
		for c := range widths {
			if w := ansi.StringWidth(styledText(k, row[c])); w > widths[c] {
				widths[c] = w
			}
		}
	}

	for c := range widths {
		widths[c] = max(widths[c], tableMinCol)
	}

	return widths
}

// styledText styles one cell's inline markers without wrapping.
func styledText(k *kit.Kit, text string) string {
	return styleSpans(k, Inline(text))
}

// tableSeparator renders the ─ row under the header, border-colored.
func tableSeparator(k *kit.Kit, widths []int) string {
	parts := make([]string, len(widths))

	for c := range widths {
		parts[c] = strings.Repeat(tableSep, widths[c])
	}

	return k.Style(kit.Border).Render("| " + strings.Join(parts, " | ") + " |")
}

// tableRow renders one grid row: cells padded to their column width
// per alignment, joined by pipes, the header row bold.
func tableRow(k *kit.Kit, aligns []Align, widths []int, cells []string, header bool) string {
	parts := make([]string, len(widths))

	for c := range widths {
		cell := styledText(k, cells[c])
		pad := widths[c] - ansi.StringWidth(cell)

		parts[c] = padCell(cell, pad, aligns[c])
	}

	row := "| " + strings.Join(parts, " | ") + " |"

	if header {
		return k.Style(kit.Fg).Bold(true).Render(row)
	}

	return row
}

// padCell pads a cell to its column width by alignment: left/right
// alignment shifts the padding to the opposite side, default and
// center split it.
func padCell(cell string, pad int, align Align) string {
	switch align {
	case AlignLeft:
		return cell + strings.Repeat(" ", pad)
	case AlignRight:
		return strings.Repeat(" ", pad) + cell
	case AlignCenter:
		left := pad / 2

		return strings.Repeat(" ", left) + cell + strings.Repeat(" ", pad-left)
	case AlignDefault:
		return cell + strings.Repeat(" ", pad)
	default:
		return cell
	}
}
