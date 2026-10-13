// Update check: compare the running version against the latest GitHub
// release, at most once per day, never blocking or breaking the launch.

package cli

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

// Update-check knobs: network budget, rate limit, and the release API.
const (
	updateCheckTimeout = 3 * time.Second
	updateCheckTTL     = 24 * time.Hour
	updateDisableEnv   = "GOLEM_DISABLE_UPDATE_CHECK"
)

// ReleasesAPI is the GitHub endpoint answering with the latest release.
const ReleasesAPI = "https://api.github.com/repos/dev-hann/golem/releases/latest"

// updateNotice is the upgrade hint printed with a newer release.
const updateNotice = "update: go install github.com/dev-hann/golem/cmd/golem@latest"

// IsNewer reports whether candidate is a strictly newer semver than
// current. Unparseable values (dev builds, git-describe snapshots) never
// trigger a notice.
func IsNewer(candidate, current string) bool {
	c, ok := parseSemver(candidate)
	if !ok {
		return false
	}

	cur, ok := parseSemver(current)
	if !ok {
		return false
	}

	for i := range c {
		if c[i] != cur[i] {
			return c[i] > cur[i]
		}
	}

	return false
}

// parseSemver accepts vX.Y.Z or X.Y.Z, returning the numeric triple.
func parseSemver(tag string) ([3]int, bool) {
	var out [3]int

	parts := strings.Split(strings.TrimPrefix(tag, "v"), ".")
	if len(parts) != 3 {
		return out, false
	}

	for i, part := range parts {
		n, err := strconv.Atoi(part)
		if err != nil || n < 0 {
			return out, false
		}

		out[i] = n
	}

	return out, true
}

// FetchLatestRelease asks the releases API for the newest tag.
func FetchLatestRelease(ctx context.Context, apiURL string) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, updateCheckTimeout)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, apiURL, http.NoBody)
	if err != nil {
		return "", fmt.Errorf("update: building request: %w", err)
	}

	req.Header.Set("Accept", "application/vnd.github+json")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return "", fmt.Errorf("update: reaching release api: %w", err)
	}

	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("update: release api status %d", resp.StatusCode)
	}

	var payload struct {
		TagName string `json:"tag_name"`
	}

	if err := json.NewDecoder(io.LimitReader(resp.Body, updateResponseLimit)).Decode(&payload); err != nil {
		return "", fmt.Errorf("update: decoding release api response: %w", err)
	}

	if payload.TagName == "" {
		return "", errors.New("update: release api returned no tag")
	}

	return payload.TagName, nil
}

// updateResponseLimit bounds the release API response read.
const updateResponseLimit = 64 * 1024

// updateStatePath is where the last successful check time is recorded.
func updateStatePath() string {
	return filepath.Join(dataDir(), "update-check")
}

// ShouldCheck reports whether a check is due at now given the state file.
func ShouldCheck(statePath string, now time.Time) bool {
	data, err := os.ReadFile(statePath) //nolint:gosec // golem's own state path
	if err != nil {
		return true
	}

	last, err := time.Parse(time.RFC3339, strings.TrimSpace(string(data)))
	if err != nil {
		return true
	}

	return now.Sub(last) >= updateCheckTTL
}

// recordCheck stamps the check time so failures stay rate-limited too.
func recordCheck(statePath string, now time.Time) error {
	if err := os.MkdirAll(filepath.Dir(statePath), 0o750); err != nil {
		return fmt.Errorf("update: creating state dir: %w", err)
	}

	if err := os.WriteFile(statePath, []byte(now.UTC().Format(time.RFC3339)), 0o600); err != nil {
		return fmt.Errorf("update: writing state: %w", err)
	}

	return nil
}

// MaybeNotifyUpdate checks for a newer release (rate-limited to one
// attempt per day, disabled via GOLEM_DISABLE_UPDATE_CHECK=1) and prints a
// one-line notice. It never returns an error: an update check must not
// break the launch, so failures are silent.
func MaybeNotifyUpdate(ctx context.Context, w io.Writer, current, apiURL string) {
	if os.Getenv(updateDisableEnv) == "1" {
		return
	}

	state := updateStatePath()
	if !ShouldCheck(state, time.Now()) {
		return
	}

	// Record the attempt regardless of outcome: offline machines must not
	// pay the timeout on every launch.
	if err := recordCheck(state, time.Now()); err != nil {
		return // no writable state dir: skip the check entirely
	}

	latest, err := FetchLatestRelease(ctx, apiURL)
	if err != nil {
		return
	}

	if IsNewer(latest, current) {
		fmt.Fprintf(w, "golem %s available (current %s) — %s\n", //nolint:errcheck // terminal output
			latest, current, updateNotice)
	}
}
