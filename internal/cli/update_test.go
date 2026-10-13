package cli_test

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/dev-hann/golem/internal/cli"
)

func TestIsNewer(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		candidate string
		current   string
		want      bool
	}{
		{name: "patch newer", candidate: "v0.1.1", current: "v0.1.0", want: true},
		{name: "minor newer", candidate: "v0.2.0", current: "v0.1.9", want: true},
		{name: "major newer", candidate: "v1.0.0", current: "v0.9.9", want: true},
		{name: "numeric not lexical", candidate: "v0.10.0", current: "v0.9.0", want: true},
		{name: "equal", candidate: "v0.1.0", current: "v0.1.0", want: false},
		{name: "older", candidate: "v0.1.0", current: "v0.2.0", want: false},
		{name: "dev current never nags", candidate: "v0.2.0", current: "dev", want: false},
		{name: "git describe current never nags", candidate: "v0.2.0", current: "v0.1.0-3-gabc", want: false},
		{name: "garbage candidate", candidate: "latest", current: "v0.1.0", want: false},
		{name: "bare tag accepted", candidate: "0.2.0", current: "v0.1.0", want: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			if got := cli.IsNewer(tt.candidate, tt.current); got != tt.want {
				t.Errorf("IsNewer(%q, %q) = %v, want %v", tt.candidate, tt.current, got, tt.want)
			}
		})
	}
}

func TestFetchLatestRelease(t *testing.T) {
	t.Parallel()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"tag_name":"v0.2.0","name":"v0.2.0"}`) //nolint:errcheck // test stub
	}))
	t.Cleanup(srv.Close)

	latest, err := cli.FetchLatestRelease(t.Context(), srv.URL)
	if err != nil {
		t.Fatalf("FetchLatestRelease: %v", err)
	}
	if latest != "v0.2.0" {
		t.Errorf("latest = %q, want v0.2.0", latest)
	}
}

func TestFetchLatestRelease_ServerError(t *testing.T) {
	t.Parallel()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	t.Cleanup(srv.Close)

	if _, err := cli.FetchLatestRelease(t.Context(), srv.URL); err == nil {
		t.Error("expected error on 500")
	}
}

func TestShouldCheck_TTL(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	state := filepath.Join(dir, "update-check")
	now := time.Now()

	if !cli.ShouldCheck(state, now) {
		t.Error("missing state file should trigger a check")
	}

	if err := os.WriteFile(state, []byte(now.UTC().Format(time.RFC3339)), 0o600); err != nil {
		t.Fatalf("write state: %v", err)
	}
	if cli.ShouldCheck(state, now) {
		t.Error("fresh stamp should suppress the check")
	}

	old := now.Add(-25 * time.Hour)
	if err := os.WriteFile(state, []byte(old.UTC().Format(time.RFC3339)), 0o600); err != nil {
		t.Fatalf("write state: %v", err)
	}
	if !cli.ShouldCheck(state, now) {
		t.Error("stamp older than a day should trigger a check")
	}

	if err := os.WriteFile(state, []byte("garbage"), 0o600); err != nil {
		t.Fatalf("write state: %v", err)
	}
	if !cli.ShouldCheck(state, now) {
		t.Error("garbage state should trigger a check")
	}
}

// updateStub serves a fixed latest tag and counts hits.
type updateStub struct {
	srv  *httptest.Server
	hits int
	tag  string
}

func newUpdateStub(t *testing.T, tag string) *updateStub {
	t.Helper()

	stub := &updateStub{tag: tag}
	stub.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		stub.hits++
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprintf(w, `{"tag_name":%q}`, tag) //nolint:errcheck // test stub
	}))
	t.Cleanup(stub.srv.Close)

	return stub
}

func TestMaybeNotifyUpdate_NotifiesOnNewerRelease(t *testing.T) {
	t.Setenv("XDG_DATA_HOME", t.TempDir())
	t.Setenv("GOLEM_DISABLE_UPDATE_CHECK", "")

	stub := newUpdateStub(t, "v9.9.9")

	var out strings.Builder
	cli.MaybeNotifyUpdate(t.Context(), &out, "v0.1.0", stub.srv.URL)

	if !strings.Contains(out.String(), "v9.9.9") || !strings.Contains(out.String(), "available") {
		t.Errorf("notice missing:\n%s", out.String())
	}
	if stub.hits != 1 {
		t.Errorf("hits = %d, want 1", stub.hits)
	}
}

func TestMaybeNotifyUpdate_SilentOnSameVersion(t *testing.T) {
	t.Setenv("XDG_DATA_HOME", t.TempDir())
	t.Setenv("GOLEM_DISABLE_UPDATE_CHECK", "")

	stub := newUpdateStub(t, "v0.1.0")

	var out strings.Builder
	cli.MaybeNotifyUpdate(t.Context(), &out, "v0.1.0", stub.srv.URL)

	if out.String() != "" {
		t.Errorf("expected no notice, got:\n%s", out.String())
	}
}

func TestMaybeNotifyUpdate_DisabledSkipsNetwork(t *testing.T) {
	t.Setenv("XDG_DATA_HOME", t.TempDir())
	t.Setenv("GOLEM_DISABLE_UPDATE_CHECK", "1")

	stub := newUpdateStub(t, "v9.9.9")

	var out strings.Builder
	cli.MaybeNotifyUpdate(t.Context(), &out, "v0.1.0", stub.srv.URL)

	if out.String() != "" {
		t.Errorf("expected no notice, got:\n%s", out.String())
	}
	if stub.hits != 0 {
		t.Errorf("hits = %d, want 0 (disabled)", stub.hits)
	}
}

func TestMaybeNotifyUpdate_RateLimited(t *testing.T) {
	t.Setenv("XDG_DATA_HOME", t.TempDir())
	t.Setenv("GOLEM_DISABLE_UPDATE_CHECK", "")

	stub := newUpdateStub(t, "v9.9.9")

	var out strings.Builder
	cli.MaybeNotifyUpdate(t.Context(), &out, "v0.1.0", stub.srv.URL)
	cli.MaybeNotifyUpdate(t.Context(), &out, "v0.1.0", stub.srv.URL)

	if stub.hits != 1 {
		t.Errorf("hits = %d, want 1 (second call rate-limited)", stub.hits)
	}
	if strings.Count(out.String(), "available") != 1 {
		t.Errorf("notice printed more than once:\n%s", out.String())
	}
}

func TestMaybeNotifyUpdate_DevBuildSilent(t *testing.T) {
	t.Setenv("XDG_DATA_HOME", t.TempDir())
	t.Setenv("GOLEM_DISABLE_UPDATE_CHECK", "")

	stub := newUpdateStub(t, "v9.9.9")

	var out strings.Builder
	cli.MaybeNotifyUpdate(t.Context(), &out, "dev", stub.srv.URL)

	if out.String() != "" {
		t.Errorf("dev builds should not be nagged, got:\n%s", out.String())
	}
}
