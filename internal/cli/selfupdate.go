// Self-update: download the latest release for this platform, verify the
// checksum, and replace the installed binaries (stdlib only).

package cli

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// updateBinaryNames are the binaries shipped inside a release tarball.
var updateBinaryNames = []string{"golem", "golem-tui"}

// updateDownloadLimit bounds tarball and checksum downloads.
const updateDownloadLimit = 128 << 20 // 128 MiB

// ReleaseAsset is one downloadable artifact of a release.
type ReleaseAsset struct {
	Name string `json:"name"`
	URL  string `json:"browser_download_url"`
}

// Release is the latest-release API payload the updater needs.
type Release struct {
	TagName string         `json:"tag_name"`
	Assets  []ReleaseAsset `json:"assets"`
}

// FetchRelease fetches the latest release including its asset list.
func FetchRelease(ctx context.Context, apiURL string) (Release, error) {
	ctx, cancel := context.WithTimeout(ctx, updateCheckTimeout)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, apiURL, http.NoBody)
	if err != nil {
		return Release{}, fmt.Errorf("update: building request: %w", err)
	}

	req.Header.Set("Accept", "application/vnd.github+json")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return Release{}, fmt.Errorf("update: reaching release api: %w", err)
	}

	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		return Release{}, fmt.Errorf("update: release api status %d", resp.StatusCode)
	}

	var release Release
	if err := json.NewDecoder(io.LimitReader(resp.Body, updateResponseLimit)).Decode(&release); err != nil {
		return Release{}, fmt.Errorf("update: decoding release api response: %w", err)
	}

	if release.TagName == "" {
		return Release{}, errors.New("update: release api returned no tag")
	}

	return release, nil
}

// UpdateOptions configures a self-update run.
type UpdateOptions struct {
	Stdout  io.Writer
	Current string
	APIURL  string
	GOOS    string
	GOARCH  string
	// InstallDir resolves where binaries land (injected for tests;
	// production: the directory of the running executable).
	InstallDir func() (string, error)
}

// RunUpdate installs the newest release for this platform. Dev snapshots
// always update; release builds skip when already current.
func RunUpdate(opts UpdateOptions) error {
	release, err := FetchRelease(context.Background(), opts.APIURL)
	if err != nil {
		return fmt.Errorf("cli: %w", err)
	}

	if isReleaseVersion(opts.Current) && !IsNewer(release.TagName, opts.Current) {
		fmt.Fprintf(opts.Stdout, "golem is up to date (%s)\n", opts.Current) //nolint:errcheck // terminal output

		return nil
	}

	fmt.Fprintf(opts.Stdout, "updating %s → %s\n", opts.Current, release.TagName) //nolint:errcheck // terminal output

	asset, err := findAsset(release, opts.GOOS, opts.GOARCH)
	if err != nil {
		return fmt.Errorf("cli: %w", err)
	}

	tarball, err := download(context.Background(), asset.URL)
	if err != nil {
		return fmt.Errorf("cli: %w", err)
	}

	checksumURL := asset.URL[:strings.LastIndex(asset.URL, "/")] + "/checksums.txt"

	checksums, err := download(context.Background(), checksumURL)
	if err != nil {
		return fmt.Errorf("cli: %w", err)
	}

	if err := verifyChecksum(tarball, asset.Name, checksums); err != nil {
		return fmt.Errorf("cli: %w", err)
	}

	fmt.Fprintln(opts.Stdout, "checksum verified ✓") //nolint:errcheck // terminal output

	dir, err := opts.InstallDir()
	if err != nil {
		return fmt.Errorf("cli: update: %w", err)
	}

	installed, err := installBinaries(tarball, dir)
	if err != nil {
		return err
	}

	fmt.Fprintf(opts.Stdout, "installed %s → %s\n", //nolint:errcheck // terminal output
		strings.Join(installed, ", "), dir)
	fmt.Fprintln(opts.Stdout, "restart the server if running: pkill -f 'golem serve'") //nolint:errcheck // terminal output

	return nil
}

// isReleaseVersion reports whether v is a plain vX.Y.Z.
func isReleaseVersion(v string) bool {
	_, ok := parseSemver(v)

	return ok
}

// findAsset picks this platform's tarball from the release.
func findAsset(release Release, goos, goarch string) (ReleaseAsset, error) {
	name := fmt.Sprintf("golem_%s_%s_%s.tar.gz",
		strings.TrimPrefix(release.TagName, "v"), goos, goarch)

	for _, asset := range release.Assets {
		if asset.Name == name {
			return asset, nil
		}
	}

	return ReleaseAsset{}, fmt.Errorf("no release asset %q for %s/%s", name, goos, goarch)
}

// download fetches a URL into memory with a size bound.
func download(ctx context.Context, url string) ([]byte, error) {
	ctx, cancel := context.WithTimeout(ctx, 60*time.Second)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, http.NoBody)
	if err != nil {
		return nil, fmt.Errorf("update: building request: %w", err)
	}

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("update: downloading %s: %w", url, err)
	}

	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("update: downloading %s: status %d", url, resp.StatusCode)
	}

	return io.ReadAll(io.LimitReader(resp.Body, updateDownloadLimit))
}

// verifyChecksum matches the tarball's sha256 against checksums.txt.
func verifyChecksum(tarball []byte, assetName string, checksums []byte) error {
	for _, line := range strings.Split(string(checksums), "\n") {
		fields := strings.Fields(line)
		if len(fields) != 2 || fields[1] != assetName {
			continue
		}

		sum := sha256.Sum256(tarball)
		if !strings.EqualFold(hex.EncodeToString(sum[:]), fields[0]) {
			return fmt.Errorf("checksum mismatch for %s — refusing to install", assetName)
		}

		return nil
	}

	return fmt.Errorf("checksums.txt has no entry for %s", assetName)
}

// installBinaries extracts the release binaries into dir, replacing any
// existing files atomically (temp file + rename).
func installBinaries(tarball []byte, dir string) ([]string, error) {
	if err := os.MkdirAll(dir, 0o750); err != nil {
		return nil, fmt.Errorf("cli: update: creating %s: %w", dir, err)
	}

	gzr, err := gzip.NewReader(bytes.NewReader(tarball))
	if err != nil {
		return nil, fmt.Errorf("cli: update: reading tarball: %w", err)
	}

	defer func() { _ = gzr.Close() }()

	var installed []string

	tr := tar.NewReader(gzr)
	for {
		header, err := tr.Next()
		if errors.Is(err, io.EOF) {
			break
		}

		if err != nil {
			return nil, fmt.Errorf("cli: update: reading tarball: %w", err)
		}

		if !wantedBinary(header.Name) {
			continue
		}

		content, err := io.ReadAll(io.LimitReader(tr, updateDownloadLimit))
		if err != nil {
			return nil, fmt.Errorf("cli: update: extracting %s: %w", header.Name, err)
		}

		// wantedBinary above pins the name to exactly "golem"/"golem-tui",
		// so no archive path can escape the install dir.
		final := filepath.Join(dir, header.Name) //nolint:gosec // name matched against a fixed allowlist
		tmp := final + ".golem-new"

		if err := os.WriteFile(tmp, content, 0o755); err != nil { //nolint:gosec // executable binary
			return nil, fmt.Errorf("cli: update: writing %s: %w", tmp, err)
		}

		if err := os.Rename(tmp, final); err != nil {
			_ = os.Remove(tmp)

			return nil, fmt.Errorf("cli: update: replacing %s: %w", final, err)
		}

		installed = append(installed, header.Name)
	}

	if len(installed) != len(updateBinaryNames) {
		return nil, fmt.Errorf("cli: update: tarball carried %v, want %v", installed, updateBinaryNames)
	}

	return installed, nil
}

// wantedBinary reports whether a tar member is a shipped binary.
func wantedBinary(name string) bool {
	for _, bin := range updateBinaryNames {
		if name == bin {
			return true
		}
	}

	return false
}
