package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"golang.org/x/net/html"
)

// WebFetch bounds: the request deadline, the response-body cap, and the
// extra bytes the truncation marker may add beyond the cap.
const (
	// WebFetchTimeout bounds one whole fetch (connect + headers + body).
	WebFetchTimeout = 30 * time.Second
	// WebFetchMaxBytes caps the response body before processing.
	WebFetchMaxBytes = 256 * 1024
)

// WebFetchTool fetches one URL over HTTP(S) with GET and returns its
// body as text: HTML collapses to visible text (script/style stripped),
// everything else passes through. It is read-only — no headers, no
// cookies, no credentials (ADR-0021).
type WebFetchTool struct{}

// webFetchArgs are the model-facing arguments of WebFetchTool.
type webFetchArgs struct {
	URL string `json:"url"`
}

// Name implements Tool.
func (WebFetchTool) Name() string { return "webfetch" }

// Description implements Tool.
func (WebFetchTool) Description() string {
	return "Fetch one http(s) URL with GET and return its text. " +
		"HTML pages return visible text (scripts and styles removed). " +
		"Read-only: 30s timeout, 256 KiB body cap."
}

// InputSchema implements Tool.
func (WebFetchTool) InputSchema() map[string]any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"url": map[string]any{
				"type":        "string",
				"description": "Absolute http:// or https:// URL to fetch.",
			},
		},
		"required": []string{"url"},
	}
}

// Execute implements Tool.
func (t WebFetchTool) Execute(ctx context.Context, args json.RawMessage) (string, error) {
	var a webFetchArgs
	if err := json.Unmarshal(args, &a); err != nil {
		return "", fmt.Errorf("webfetch: parsing args: %w", err)
	}

	if strings.TrimSpace(a.URL) == "" {
		return "", fmt.Errorf("webfetch: missing url")
	}

	url := strings.TrimSpace(a.URL)

	if err := requireHTTPScheme(url); err != nil {
		return "", fmt.Errorf("webfetch: %w", err)
	}

	ctx, cancel := context.WithTimeout(ctx, WebFetchTimeout)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, http.NoBody)
	if err != nil {
		return "", fmt.Errorf("webfetch: building request: %w", err)
	}

	req.Header.Set("User-Agent", "golem-agent (webfetch)")

	client := &http.Client{
		Timeout: WebFetchTimeout,
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			if err := requireHTTPScheme(req.URL.String()); err != nil {
				return fmt.Errorf("redirect to %q: %w", req.URL.Scheme, err)
			}

			if len(via) >= maxRedirects {
				return fmt.Errorf("too many redirects (> %d)", maxRedirects)
			}

			return nil
		},
	}

	resp, err := client.Do(req) //nolint:bodyclose // closed below via defer
	if err != nil {
		return "", fmt.Errorf("webfetch: %w", err)
	}
	defer resp.Body.Close() //nolint:errcheck // read-only handle

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return "", fmt.Errorf("webfetch: status %s", resp.Status)
	}

	body, err := io.ReadAll(io.LimitReader(resp.Body, WebFetchMaxBytes+1))
	if err != nil {
		return "", fmt.Errorf("webfetch: reading body: %w", err)
	}

	oversize := len(body) > WebFetchMaxBytes
	if oversize {
		body = body[:WebFetchMaxBytes]
	}

	contentType := resp.Header.Get("Content-Type")

	text := string(body)
	if strings.Contains(contentType, "html") || looksLikeHTML(text) {
		text = htmlToText(body)
	}

	if oversize {
		text += truncateMarker
	}

	// HTML→text shrinks; the cap still guards non-HTML bodies.
	clipped, _ := Truncate(text, WebFetchMaxBytes)

	return clipped, nil
}

// maxRedirects bounds the redirect chain length.
const maxRedirects = 10

// requireHTTPScheme enforces that url is an absolute http(s) URL — the
// read-only contract refuses every other scheme (file:, ftp:, data:…).
func requireHTTPScheme(url string) error {
	for _, scheme := range []string{"http://", "https://"} {
		if strings.HasPrefix(strings.ToLower(url), scheme) {
			return nil
		}
	}

	return fmt.Errorf("url must be http(s), got %q", firstSchemeOf(url))
}

// firstSchemeOf extracts the scheme prefix for error messages ("" when
// the url carries none).
func firstSchemeOf(url string) string {
	if i := strings.Index(url, ":"); i > 0 {
		return url[:i]
	}

	return url
}

// looksLikeHTML sniffs tag-shaped content when no Content-Type says so —
// servers lie often enough that a cheap check beats trusting headers.
func looksLikeHTML(body string) bool {
	head := strings.ToLower(body[:min(len(body), 1024)])

	return strings.Contains(head, "<!doctype html") ||
		strings.Contains(head, "<html") ||
		strings.Contains(head, "<body") ||
		strings.Contains(head, "<div") ||
		strings.Contains(head, "<p>") ||
		(strings.Contains(head, "<head") && strings.Contains(head, "<title"))
}

// htmlToText extracts the visible text of an HTML document: script,
// style, and noscript contents are removed; text nodes are collected
// with whitespace collapsed. Markup that fails to parse falls back to a
// tag-strip pass rather than erroring.
func htmlToText(body []byte) string {
	root, err := html.Parse(strings.NewReader(string(body)))
	if err != nil {
		return stripTags(string(body))
	}

	var b strings.Builder
	walkVisibleText(&b, root)

	return collapseWhitespace(b.String())
}

// walkVisibleText appends text-node content of n and its descendants,
// skipping the invisible containers (script, style, noscript, head).
func walkVisibleText(b *strings.Builder, n *html.Node) {
	if n.Type == html.ElementNode {
		switch n.Data {
		case "script", "style", "noscript", "head", "template":
			return
		}
	}

	if n.Type == html.TextNode {
		b.WriteString(n.Data)
		b.WriteString(" ")
	}

	for c := n.FirstChild; c != nil; c = c.NextSibling {
		walkVisibleText(b, c)
	}
}

// stripTags is the fallback text extractor: it drops everything between
// < and > and collapses whitespace — crude, but never worse than the
// raw markup it replaces.
func stripTags(s string) string {
	var b strings.Builder

	depth := 0

	for _, r := range s {
		switch {
		case r == '<':
			depth++
		case r == '>':
			if depth > 0 {
				depth--
			}
		case depth == 0:
			b.WriteRune(r)
		}
	}

	return collapseWhitespace(b.String())
}

// collapseWhitespace trims and squeezes runs of whitespace to single
// spaces, keeping the result one scannable block.
func collapseWhitespace(s string) string {
	return strings.Join(strings.Fields(s), " ")
}
