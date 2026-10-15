// Package markdown renders assistant text as terminal markdown. It is
// a view concern only: the wire protocol and JSONL storage stay raw
// text (ADR-0026). The grammar is a block-level subset — paragraph,
// heading, fenced code, list, quote, rule, table — plus
// bold/italic/code/link inline markers, everything else falling back to
// raw text.
package markdown

import "strings"

// BlockKind enumerates parsed block types.
type BlockKind int

// The v1 block grammar.
const (
	BlockParagraph BlockKind = iota
	BlockHeading
	BlockCode
	BlockList
	BlockQuote
	BlockRule
	BlockTable
)

// Align is a table column's alignment.
type Align int

// The GFM delimiter-row alignments.
const (
	AlignDefault Align = iota
	AlignLeft
	AlignRight
	AlignCenter
)

// Block is one parsed markdown block. Text holds the block's body with
// markers stripped (code keeps its inner lines verbatim).
type Block struct {
	Kind BlockKind
	// Level is the heading depth (1-4; deeper markers stay paragraphs).
	Level int
	// Lang labels a fenced code block's language ("" when bare).
	Lang string
	// Text is the block body. For code it ends with a newline per line.
	Text string
	// Ordered distinguishes numbered lists from bullet lists.
	Ordered bool
	// Items lists the entries of a list block.
	Items []string
	// Cells holds a table's rows: row 0 is the header, row 1+ the
	// body. Every row has exactly len(Align) cells.
	Cells [][]string
	// Align lists a table's per-column alignment from its delimiter
	// row.
	Align []Align
	// settled marks the final block of text that ended in a newline:
	// the paragraph cannot grow, so it is cacheable. Open code fences
	// are never settled.
	settled bool
}

// headingDepth reports the heading level of line (1-4), or 0 when the
// line is not a heading. Deeper markers are left to the paragraph path
// so they render literally.
func headingDepth(line string) int {
	depth := 0
	for depth < len(line) && line[depth] == '#' {
		depth++
	}

	if depth == 0 || depth > maxHeading {
		return 0
	}

	rest := line[depth:]
	if !strings.HasPrefix(rest, " ") || strings.TrimSpace(rest) == "" {
		return 0
	}

	return depth
}

// maxHeading is the deepest heading level rendered as a heading.
const maxHeading = 4

// Parse splits text into blocks. Trailing incomplete structure (an open
// fence, or a paragraph whose closing newline has not streamed yet) is
// marked Open so the renderer can keep re-evaluating it; text that ends
// in a newline has settled its final block.
func Parse(text string) []Block {
	s := &blockScanner{
		lines:   strings.Split(strings.TrimSuffix(text, "\n"), "\n"),
		settled: strings.HasSuffix(text, "\n"),
	}

	for i := 0; i < len(s.lines); {
		i = s.consume(i)
	}

	s.flushPara()

	if s.settled && len(s.blocks) > 0 {
		s.blocks[len(s.blocks)-1].settled = true
	}

	return s.blocks
}

// blockScanner holds one Parse run's accumulators.
type blockScanner struct {
	lines   []string
	blocks  []Block
	para    []string
	settled bool
}

// consume handles lines[i], returning the next unconsumed index.
func (s *blockScanner) consume(i int) int {
	line := s.lines[i]

	switch {
	case strings.TrimSpace(line) == "":
		s.flushPara()

		return i + 1
	case isFence(line):
		return s.consumeFence(i)
	case line == "---" || line == "***":
		s.flushPara()
		s.blocks = append(s.blocks, Block{Kind: BlockRule})

		return i + 1
	case strings.HasPrefix(line, "> "):
		return s.consumeQuote(i)
	case headingDepth(line) > 0:
		return s.consumeHeading(line, i)
	case listItem(line) != nil:
		return s.consumeList(i)
	case isPipeRow(line) && i+1 < len(s.lines) && isDelimiterRow(s.lines[i+1]):
		return s.consumeTable(i)
	default:
		s.para = append(s.para, line)

		return i + 1
	}
}

// consumeHeading consumes one heading line.
func (s *blockScanner) consumeHeading(line string, i int) int {
	s.flushPara()
	s.blocks = append(s.blocks, Block{
		Kind:  BlockHeading,
		Level: headingDepth(line),
		Text:  strings.TrimSpace(strings.TrimLeft(line, "#")),
	})

	return i + 1
}

// consumeFence consumes one fenced code block from lines[i].
func (s *blockScanner) consumeFence(i int) int {
	s.flushPara()

	body, lang, next, closed := readFence(s.lines, i)
	s.blocks = append(s.blocks, Block{Kind: BlockCode, Lang: lang, Text: body, settled: closed})

	return next
}

// consumeQuote consumes one quote block from lines[i].
func (s *blockScanner) consumeQuote(i int) int {
	s.flushPara()

	quote, next := readQuote(s.lines, i)
	s.blocks = append(s.blocks, Block{Kind: BlockQuote, Text: quote})

	return next
}

// consumeList consumes one list block from lines[i].
func (s *blockScanner) consumeList(i int) int {
	s.flushPara()

	items, ordered, next := readList(s.lines, i)
	s.blocks = append(s.blocks, Block{Kind: BlockList, Ordered: ordered, Items: items})

	return next
}

// consumeTable consumes one table block from lines[i] (the header row;
// the delimiter row at i+1 is validated by the caller). Consecutive
// pipe rows after the delimiter are body rows; the first non-pipe line
// ends the table.
func (s *blockScanner) consumeTable(i int) int {
	s.flushPara()

	align := delimiterAlign(s.lines[i+1])
	cols := len(align)

	rows := [][]string{splitCells(s.lines[i], cols)}

	for r := i + 2; r < len(s.lines); r++ {
		if !isPipeRow(s.lines[r]) {
			break
		}

		rows = append(rows, splitCells(s.lines[r], cols))
	}

	s.blocks = append(s.blocks, Block{Kind: BlockTable, Cells: rows, Align: align})

	return i + 1 + len(rows)
}

// isPipeRow reports whether line is a table row: a non-empty pipe line.
func isPipeRow(line string) bool {
	return line != "" && strings.HasPrefix(line, "|")
}

// splitCells cuts one pipe row into exactly cols trimmed cells: leading
// and trailing pipes drop, extras truncate, missing cells pad empty.
func splitCells(line string, cols int) []string {
	row := strings.TrimSuffix(strings.TrimPrefix(line, "|"), "|")
	raw := strings.Split(row, "|")

	cells := make([]string, cols)

	for c := range cells {
		if c < len(raw) {
			cells[c] = strings.TrimSpace(raw[c])
		}
	}

	return cells
}

// isDelimiterRow reports whether line is a GFM delimiter row
// ("| --- | :---: | ---: |"): at least one column, every cell only
// dashes with optional colons.
func isDelimiterRow(line string) bool {
	if !isPipeRow(line) {
		return false
	}

	cells := rawCells(line)
	if len(cells) == 0 {
		return false
	}

	for _, cell := range cells {
		if !isDelimiterCell(cell) {
			return false
		}
	}

	return true
}

// rawCells cuts one pipe row into its actual cells — no padding, no
// truncation — trimmed.
func rawCells(line string) []string {
	row := strings.TrimSuffix(strings.TrimPrefix(line, "|"), "|")

	cells := strings.Split(row, "|")

	for i := range cells {
		cells[i] = strings.TrimSpace(cells[i])
	}

	return cells
}

// isDelimiterCell reports whether cell is dashes with optional leading
// and/or trailing colons ("---", ":---", "---:", ":---:").
func isDelimiterCell(cell string) bool {
	cell = strings.TrimPrefix(cell, ":")
	cell = strings.TrimSuffix(cell, ":")

	return cell != "" && strings.Trim(cell, "-") == ""
}

// delimiterAlign parses the per-column alignments from a delimiter row.
func delimiterAlign(line string) []Align {
	cells := rawCells(line)

	align := make([]Align, 0, len(cells))

	for _, cell := range cells {
		align = append(align, cellAlign(cell))
	}

	return align
}

// cellAlign maps one delimiter cell to its alignment.
func cellAlign(cell string) Align {
	left := strings.HasPrefix(cell, ":")
	right := strings.HasSuffix(cell, ":")

	switch {
	case left && right:
		return AlignCenter
	case left:
		return AlignLeft
	case right:
		return AlignRight
	default:
		return AlignDefault
	}
}

// flushPara closes the pending paragraph, if any.
func (s *blockScanner) flushPara() {
	if len(s.para) == 0 {
		return
	}

	// Line breaks inside a paragraph are preserved: in a terminal
	// chat the model's newlines are deliberate formatting, not
	// CommonMark soft breaks to collapse.
	s.blocks = append(s.blocks, Block{Kind: BlockParagraph, Text: strings.Join(s.para, "\n")})
	s.para = nil
}

// Split partitions parsed blocks into completed (cacheable) and live
// (re-rendered per delta). The tail is live when the final block may
// still grow: a paragraph not followed by a blank line, or a code block
// whose closing fence has not arrived.
func Split(blocks []Block) (completed, live []Block) {
	if len(blocks) == 0 {
		return nil, nil
	}

	if blocks[len(blocks)-1].settled {
		return blocks, nil
	}

	return blocks[:len(blocks)-1], blocks[len(blocks)-1:]
}

// isFence reports whether line opens a code fence.
func isFence(line string) bool {
	return strings.HasPrefix(line, "```")
}

// readFence consumes one fenced block from lines[start]. It returns the
// body, the language label, the index of the first unconsumed line, and
// whether a closing fence arrived. An unclosed fence consumes the rest
// of the input (streaming rule).
func readFence(lines []string, start int) (body, lang string, next int, closed bool) {
	lang = strings.TrimSpace(strings.TrimPrefix(lines[start], "```"))

	var b strings.Builder

	for i := start + 1; i < len(lines); i++ {
		if isFence(lines[i]) {
			return b.String(), lang, i + 1, true
		}

		b.WriteString(lines[i])
		b.WriteString("\n")
	}

	return b.String(), lang, len(lines), false
}

// readQuote consumes consecutive "> " lines from start.
func readQuote(lines []string, start int) (quote string, next int) {
	var b strings.Builder

	for i := start; i < len(lines); i++ {
		line, ok := strings.CutPrefix(lines[i], "> ")
		if !ok {
			return strings.TrimSuffix(b.String(), "\n"), i
		}

		b.WriteString(line)
		b.WriteString("\n")
	}

	return strings.TrimSuffix(b.String(), "\n"), len(lines)
}

// readList consumes consecutive list lines from start. Ordered markers
// may number anything (1. 2. 3. …); the block stays ordered from its
// first line.
func readList(lines []string, start int) (items []string, ordered bool, next int) {
	first := listItem(lines[start])
	if first == nil {
		return nil, false, start
	}

	ordered = isOrderedMarker(lines[start])

	for i := start; i < len(lines); i++ {
		item := listItem(lines[i])
		if item == nil || isOrderedMarker(lines[i]) != ordered {
			return items, ordered, i
		}

		items = append(items, *item)
	}

	return items, ordered, len(lines)
}

// isOrderedMarker reports whether line opens with "N. ".
func isOrderedMarker(line string) bool {
	i := strings.Index(line, ". ")
	return i > 0 && isDigits(line[:i])
}

// listItem parses one list line ("- x" or "N. x"); nil when the line is
// not a list entry.
func listItem(line string) *string {
	if item, ok := strings.CutPrefix(line, "- "); ok {
		return &item
	}

	if i := strings.Index(line, ". "); i > 0 && isDigits(line[:i]) {
		item := line[i+2:]
		return &item
	}

	return nil
}

// isDigits reports whether s is all ASCII digits, non-empty.
func isDigits(s string) bool {
	if s == "" {
		return false
	}

	for _, c := range s {
		if c < '0' || c > '9' {
			return false
		}
	}

	return true
}
