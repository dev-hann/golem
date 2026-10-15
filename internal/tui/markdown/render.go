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
