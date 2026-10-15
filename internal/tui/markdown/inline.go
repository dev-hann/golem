package markdown

import (
	"strings"
	"unicode/utf8"
)

// Span is one inline-styled run of text.
type Span struct {
	Text   string
	Bold   bool
	Italic bool
	Code   bool
	Link   bool
	Dim    bool
}

// Inline parses inline markers in one line of text. Unclosed markers
// render literally (the streaming-safe remend replacement: never guess
// a close that has not arrived), and backslash escapes drop their
// backslash.
func Inline(text string) []Span {
	return parseInline(text, Span{})
}

// parseInline renders text whose runs inherit wrap's emphasis. Nested
// markers parse recursively one level at a time.
func parseInline(text string, wrap Span) []Span {
	p := &inlineParser{wrap: wrap}
	p.run(text)

	return p.spans
}

// inlineParser accumulates spans for one text run.
type inlineParser struct {
	spans []Span
	plain strings.Builder
	wrap  Span
}

// run parses text into the parser's spans.
func (p *inlineParser) run(text string) {
	rest := text

	for rest != "" {
		next, ok := p.step(rest)
		if !ok {
			break
		}

		rest = next
	}

	p.commit()
}

// step consumes one marker or rune from rest, reporting the remainder;
// false means nothing was consumed.
func (p *inlineParser) step(rest string) (string, bool) {
	switch {
	case strings.HasPrefix(rest, `\`):
		return p.escape(rest)
	case strings.HasPrefix(rest, "**"):
		return p.emphasis(rest, "**", Span{Bold: true})
	case strings.HasPrefix(rest, "*"):
		return p.emphasis(rest, "*", Span{Italic: true})
	case rest[0] == '`':
		return p.codeSpan(rest)
	case rest[0] == '[':
		return p.link(rest)
	default:
		_, size := utf8.DecodeRuneInString(rest)
		p.plain.WriteString(rest[:size])

		return rest[size:], true
	}
}

// escape consumes a backslash escape: the backslash drops, the escaped
// character stays literal (a lone trailing backslash stays too).
func (p *inlineParser) escape(rest string) (string, bool) {
	if len(rest) >= 2 {
		p.plain.WriteString(rest[1:2])

		return rest[2:], true
	}

	p.plain.WriteString(`\`)

	return rest[1:], true
}

// emphasis consumes one emphasis marker pair, recursing into the inner
// text with the accumulated style. Unclosed markers render literally.
func (p *inlineParser) emphasis(rest, marker string, style Span) (string, bool) {
	inner, ok := closedAfter(rest, len(marker), marker)
	if !ok {
		p.plain.WriteString(marker)

		return rest[len(marker):], true
	}

	p.commit()

	nested := p.wrap
	nested.Bold = nested.Bold || style.Bold
	nested.Italic = nested.Italic || style.Italic
	p.spans = append(p.spans, parseInline(inner, nested)...)

	return rest[len(inner)+2*len(marker):], true
}

// codeSpan consumes one backtick pair; unclosed backticks stay literal.
func (p *inlineParser) codeSpan(rest string) (string, bool) {
	end := strings.IndexByte(rest[1:], '`')
	if end < 0 {
		p.plain.WriteByte('`')

		return rest[1:], true
	}

	p.commit()
	p.spans = append(p.spans, mergeWrap(Span{Text: rest[1 : 1+end], Code: true}, p.wrap))

	return rest[1+end+1:], true
}

// link consumes one "[label](target)" pair; unclosed shapes stay
// literal. The label renders in the link style, the target dim in
// parentheses after it.
func (p *inlineParser) link(rest string) (string, bool) {
	label, target, width, ok := closedLink(rest)
	if !ok {
		p.plain.WriteByte('[')

		return rest[1:], true
	}

	p.commit()
	p.spans = append(p.spans,
		mergeWrap(Span{Text: label, Link: true}, p.wrap),
		mergeWrap(Span{Text: " (" + target + ")", Dim: true}, p.wrap),
	)

	return rest[width:], true
}

// commit flushes the pending plain run into the span list, inheriting
// the accumulated wrapper emphasis.
func (p *inlineParser) commit() {
	if p.plain.Len() == 0 {
		return
	}

	p.spans = append(p.spans, mergeWrap(Span{Text: p.plain.String()}, p.wrap))
	p.plain.Reset()
}

// mergeWrap folds a nested style into an outer wrapper's emphasis.
func mergeWrap(s, wrap Span) Span {
	s.Bold = s.Bold || wrap.Bold
	s.Italic = s.Italic || wrap.Italic

	return s
}

// closedAfter reports whether s[openLen:] contains a closer with at
// least one character before it, returning the inner text.
func closedAfter(s string, openLen int, closer string) (inner string, ok bool) {
	rest := s[openLen:]
	if idx := strings.Index(rest, closer); idx > 0 {
		return rest[:idx], true
	}

	return "", false
}

// closedLink parses "[label](target)" at the start of s, returning the
// label, target, the consumed width, and whether it closed.
func closedLink(s string) (label, target string, width int, ok bool) {
	end := strings.IndexByte(s, ']')
	if end < 1 || end+1 >= len(s) || s[end+1] != '(' {
		return "", "", 0, false
	}

	urlEnd := strings.IndexByte(s[end+1:], ')')
	if urlEnd < 1 {
		return "", "", 0, false
	}

	label, target = s[1:end], s[end+2:end+1+urlEnd]

	if label == "" || target == "" || strings.ContainsAny(label+target, "\n") {
		return "", "", 0, false
	}

	return label, target, end + 1 + urlEnd + 1, true
}
