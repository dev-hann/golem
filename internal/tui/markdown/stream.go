package markdown

import (
	"strings"

	"github.com/dev-hann/golem/internal/tui/kit"
)

// renderFn renders one whole message (test seam: counts calls).
type renderFn func(k *kit.Kit, width int, text string) string

// Renderer renders streaming markdown with a completed-prefix cache
// (ADR-0026): the portion of a message already terminated (everything
// through the last blank line outside an open code fence) renders once
// per content+width; only the trailing live block re-renders per delta.
type Renderer struct {
	render renderFn

	// cache maps prefix text+width to its rendered output.
	cache map[string]string
}

// NewRenderer builds a streaming renderer around a whole-message render
// function (tests inject counters; production passes Render).
func NewRenderer(render renderFn) *Renderer {
	return &Renderer{render: render, cache: map[string]string{}}
}

// Render renders text at width through the completed-prefix cache.
func (r *Renderer) Render(k *kit.Kit, width int, text string) string {
	prefix, live := splitRaw(text)
	if prefix == "" {
		return r.render(k, width, text)
	}

	out, ok := r.cache[cacheKey(prefix, width)]
	if !ok {
		out = r.render(k, width, prefix)
		r.cache[cacheKey(prefix, width)] = out
	}

	if live == "" {
		return out
	}

	return out + "\n\n" + r.render(k, width, live)
}

// cacheKey composes the cache key from content and width.
func cacheKey(text string, width int) string {
	return text + "\x00" + itoa(width)
}

// itoa is the minimal width formatter (fmt kept out of the hot path).
func itoa(n int) string {
	if n == 0 {
		return "0"
	}

	var digits []byte
	for n > 0 {
		digits = append([]byte{byte('0' + n%10)}, digits...)
		n /= 10
	}

	return string(digits)
}

// splitRaw partitions raw text into (completed prefix, live tail). The
// boundary is the last blank line with structure after it — unless the
// text has settled (ends in a newline, no open fence), in which case
// everything is completed and the tail is empty. An open code fence
// swallows blank lines, so its opening line starts the tail.
func splitRaw(text string) (prefix, live string) {
	if at := openFenceStart(text); at >= 0 {
		return text[:at], text[at:]
	}

	idx := strings.LastIndex(text, "\n\n")
	if idx < 0 {
		// A single block with no blank line: live unless settled by a
		// trailing newline.
		if strings.HasSuffix(text, "\n") {
			return text, ""
		}

		return "", text
	}

	tail := text[idx+2:]

	// A tail that itself ends in a newline has settled: the final block
	// cannot grow (a new block would start after a blank line).
	if strings.HasSuffix(tail, "\n") {
		return text, ""
	}

	return text[:idx+2], tail
}

// openFenceStart reports the offset where an unclosed code fence begins
// (fenced content may contain blank lines), or -1 when every fence
// closed.
func openFenceStart(text string) int {
	lines := strings.Split(text, "\n")

	openAt := -1

	for i, line := range lines {
		if !strings.HasPrefix(line, "```") {
			continue
		}

		if openAt >= 0 {
			openAt = -1 // this line closes the fence
		} else {
			openAt = i
		}
	}

	if openAt < 0 {
		return -1
	}

	offset := 0

	for i := range openAt {
		offset += len(lines[i]) + 1
	}

	return offset
}
