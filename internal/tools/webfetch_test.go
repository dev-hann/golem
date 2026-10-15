package tools_test

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/dev-hann/golem/internal/tools"
)

// webFetchCases covers the read-only fetch contract: text passes through,
// HTML collapses to visible text, oversize bodies clip, non-HTTP schemes
// and post-redirect scheme switches are refused.
func TestWebFetch(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name    string
		handler http.HandlerFunc
		setup   func(t *testing.T) string // returns the URL to fetch
		want    string
		wantErr string
	}{
		{
			name: "plain text passes through",
			handler: func(w http.ResponseWriter, _ *http.Request) {
				w.Header().Set("Content-Type", "text/plain")
				fmt.Fprint(w, "hello plain")
			},
			want: "hello plain",
		},
		{
			name: "html collapses to visible text",
			handler: func(w http.ResponseWriter, _ *http.Request) {
				w.Header().Set("Content-Type", "text/html")
				fmt.Fprint(w, `<!DOCTYPE html><html><head><title>T</title><script>var x=1;</script><style>.a{color:red}</style></head><body><h1>Heading</h1><p>Some &amp; text</p><noscript>no</noscript></body></html>`)
			},
			want: "Heading",
		},
		{
			name: "script and style content is stripped",
			handler: func(w http.ResponseWriter, _ *http.Request) {
				w.Header().Set("Content-Type", "text/html")
				fmt.Fprint(w, `<html><body><script>SECRETSCRIPT</script><style>SECRETSTYLE</style><p>visible</p></body></html>`)
			},
			want: "visible",
		},
		{
			name: "oversize body clips with a marker",
			handler: func(w http.ResponseWriter, _ *http.Request) {
				w.Header().Set("Content-Type", "text/plain")
				fmt.Fprint(w, strings.Repeat("x", tools.WebFetchMaxBytes+1024))
			},
			want: "[truncated]",
		},
		{
			name: "json passes through as text",
			handler: func(w http.ResponseWriter, _ *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				fmt.Fprint(w, `{"a":1}`)
			},
			want: `{"a":1}`,
		},
		{
			name:    "file scheme is refused",
			setup:   func(*testing.T) string { return "file:///etc/passwd" },
			wantErr: "http",
		},
		{
			name:    "ftp scheme is refused",
			setup:   func(*testing.T) string { return "ftp://example.com/f" },
			wantErr: "http",
		},
		{
			name:    "empty url is refused",
			setup:   func(*testing.T) string { return "" },
			wantErr: "url",
		},
		{
			name: "redirect to a non-http scheme is refused",
			setup: func(t *testing.T) string {
				t.Helper()
				srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					http.Redirect(w, r, "file:///etc/passwd", http.StatusFound)
				}))
				t.Cleanup(srv.Close)

				return srv.URL
			},
			wantErr: "http",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			url := ""
			if tc.setup != nil {
				url = tc.setup(t)
			} else {
				srv := httptest.NewServer(tc.handler)
				t.Cleanup(srv.Close)
				url = srv.URL
			}

			tool := tools.WebFetchTool{}
			got, err := tool.Execute(t.Context(),
				[]byte(fmt.Sprintf(`{"url":%q}`, url)))

			switch {
			case tc.wantErr != "":
				if err == nil {
					t.Fatalf("Execute succeeded, want error containing %q (got %q)", tc.wantErr, got)
				}
				if !strings.Contains(err.Error(), tc.wantErr) {
					t.Errorf("error = %v, want it to mention %q", err, tc.wantErr)
				}
			case err != nil:
				t.Fatalf("Execute: %v", err)
			case !strings.Contains(got, tc.want):
				t.Errorf("result missing %q, got:\n%.200s", tc.want, got)
			}
		})
	}
}

func TestWebFetch_IsReadOnly(t *testing.T) {
	t.Parallel()

	var (
		seen atomic.Int64
		srv  = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			seen.Add(1)
			if r.Method != http.MethodGet {
				t.Errorf("method = %s, want GET only", r.Method)
			}
			fmt.Fprint(w, "ok")
		}))
	)
	t.Cleanup(srv.Close)

	tool := tools.WebFetchTool{}
	if _, err := tool.Execute(t.Context(),
		[]byte(fmt.Sprintf(`{"url":%q}`, srv.URL))); err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if seen.Load() == 0 {
		t.Error("handler never hit")
	}
}
