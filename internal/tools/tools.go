// Package tools implements golem's built-in agent tools. Tools are pure
// executors: they never call providers and know nothing about the agent loop.
package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"path/filepath"
	"sort"
	"strings"

	"github.com/golem-agent/golem/internal/llm"
)

// DefaultOutputBudget is the byte cap on a tool result before it enters model
// context. See docs/CONFIG.md ([tools].output_budget_bytes).
const DefaultOutputBudget = 16 * 1024

// Tool is a unit of work the agent can dispatch by name.
type Tool interface {
	// Name returns the registry key for this tool.
	Name() string
	// Description is what the model sees.
	Description() string
	// InputSchema is the JSON Schema (object) the model's arguments must match.
	InputSchema() map[string]any
	// Execute runs the tool. A returned error is reported to the model as a
	// failed tool result; it does not abort the agent loop.
	Execute(ctx context.Context, args json.RawMessage) (string, error)
}

// Registry indexes tools by name.
type Registry struct {
	byName map[string]Tool
}

// NewRegistry builds a registry from the given tools. Duplicate names are an
// error so that tool specs presented to providers stay unambiguous.
func NewRegistry(toolList ...Tool) (*Registry, error) {
	byName := make(map[string]Tool, len(toolList))
	for _, t := range toolList {
		if t.Name() == "" {
			return nil, fmt.Errorf("tools: tool with empty name (%T)", t)
		}

		if _, dup := byName[t.Name()]; dup {
			return nil, fmt.Errorf("tools: duplicate tool name %q", t.Name())
		}

		byName[t.Name()] = t
	}

	return &Registry{byName: byName}, nil
}

// Get looks a tool up by name.
func (r *Registry) Get(name string) (Tool, bool) {
	t, ok := r.byName[name]
	return t, ok
}

// Specs returns provider-facing specs for all registered tools, sorted by name
// so generated requests are deterministic.
func (r *Registry) Specs() []llm.ToolSpec {
	specs := make([]llm.ToolSpec, 0, len(r.byName))

	names := make([]string, 0, len(r.byName))
	for name := range r.byName {
		names = append(names, name)
	}

	sort.Strings(names)

	for _, name := range names {
		t := r.byName[name]
		specs = append(specs, llm.ToolSpec{
			Name:        t.Name(),
			Description: t.Description(),
			InputSchema: t.InputSchema(),
		})
	}

	return specs
}

// Execute dispatches to a registered tool. Unknown tools return an error so
// the model can correct itself on the next round.
func (r *Registry) Execute(ctx context.Context, name string, args json.RawMessage) (string, error) {
	t, ok := r.byName[name]
	if !ok {
		return "", fmt.Errorf("unknown tool %q", name)
	}

	return t.Execute(ctx, args)
}

// truncateMarker is appended when a result exceeds the byte budget.
const truncateMarker = "\n...[truncated]"

// Truncate cuts s to at most budget bytes, backing off to a UTF-8 boundary and
// appending a marker. The second return value reports whether truncation
// happened. budget must be positive; callers use DefaultOutputBudget.
func Truncate(s string, budget int) (string, bool) {
	if budget <= 0 || len(s) <= budget {
		return s, false
	}

	cut := budget
	for cut > 0 && !utf8RuneStart(s[cut]) {
		cut--
	}

	return s[:cut] + truncateMarker, true
}

// utf8RuneStart reports whether b is the first byte of a UTF-8 encoded rune.
func utf8RuneStart(b byte) bool {
	return b&0xC0 != 0x80
}

// resolvePath anchors a possibly-relative argument path at root and enforces
// that the result stays inside root. Path traversal outside the workspace is
// an error, not a panic.
func resolvePath(root, arg string) (string, error) {
	rootAbs, err := filepath.Abs(root)
	if err != nil {
		return "", fmt.Errorf("resolving root %q: %w", root, err)
	}

	joined := arg
	if !filepath.IsAbs(arg) {
		joined = filepath.Join(rootAbs, arg)
	}

	abs, err := filepath.Abs(joined)
	if err != nil {
		return "", fmt.Errorf("resolving %q: %w", arg, err)
	}

	if abs != rootAbs && !strings.HasPrefix(abs+string(filepath.Separator), rootAbs+string(filepath.Separator)) {
		return "", fmt.Errorf("path %q escapes workspace root %q", arg, root)
	}

	return abs, nil
}
