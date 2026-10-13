package cli_test

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/dev-hann/golem/internal/cli"
)

// buildTarGz packs files into a release-shaped tarball.
func buildTarGz(t *testing.T, files map[string]string) []byte {
	t.Helper()

	var buf bytes.Buffer
	gzw := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gzw)

	for name, content := range files {
		if err := tw.WriteHeader(&tar.Header{
			Name: name, Mode: 0o755, Size: int64(len(content)),
		}); err != nil {
			t.Fatalf("tar header: %v", err)
		}

		if _, err := tw.Write([]byte(content)); err != nil {
			t.Fatalf("tar write: %v", err)
		}
	}

	if err := tw.Close(); err != nil {
		t.Fatalf("tar close: %v", err)
	}

	if err := gzw.Close(); err != nil {
		t.Fatalf("gzip close: %v", err)
	}

	return buf.Bytes()
}

// releaseServer serves a full update flow: release JSON, tarball, checksums.
type releaseServer struct {
	srv      *httptest.Server
	apiHits  int
	assetHex string
	asset    string
	corrupt  bool
}

func newReleaseServer(t *testing.T, tarball []byte) *releaseServer {
	t.Helper()

	rs := &releaseServer{asset: "golem_9.9.9_linux_amd64.tar.gz"}
	sum := sha256.Sum256(tarball)
	rs.assetHex = hex.EncodeToString(sum[:])

	mux := http.NewServeMux()
	rs.srv = httptest.NewServer(mux)
	t.Cleanup(rs.srv.Close)

	assetURL := rs.srv.URL + "/dl/" + rs.asset

	mux.HandleFunc("/api", func(w http.ResponseWriter, _ *http.Request) {
		rs.apiHits++

		w.Header().Set("Content-Type", "application/json")
		fmt.Fprintf(w, `{"tag_name":"v9.9.9","assets":[{"name":%q,"browser_download_url":%q}]}`, //nolint:errcheck // test stub
			rs.asset, assetURL)
	})

	mux.HandleFunc("/dl/", func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "checksums.txt") {
			digest := rs.assetHex
			if rs.corrupt {
				digest = "0000000000000000000000000000000000000000000000000000000000000000"
			}

			w.Write([]byte(digest + "  " + rs.asset + "\n")) //nolint:errcheck // test stub

			return
		}

		w.Write(tarball) //nolint:errcheck // test stub
	})

	return rs
}

// runUpdate executes the updater against the stub with temp install dir.
func runUpdate(t *testing.T, rs *releaseServer, current string) (output string, installed []string) {
	t.Helper()

	var out strings.Builder

	dir := t.TempDir()

	err := cli.RunUpdate(cli.UpdateOptions{
		Stdout:  &out,
		Current: current,
		APIURL:  rs.srv.URL + "/api",
		GOOS:    "linux",
		GOARCH:  "amd64",
		InstallDir: func() (string, error) {
			return dir, nil
		},
	})
	if err != nil {
		t.Fatalf("RunUpdate: %v (output: %s)", err, out.String())
	}

	return out.String(), []string{filepath.Join(dir, "golem"), filepath.Join(dir, "golem-tui")}
}

func TestRunUpdate_InstallsVerifiedBinaries(t *testing.T) {
	t.Parallel()

	tarball := buildTarGz(t, map[string]string{
		"golem":     "new-golem-bin",
		"golem-tui": "new-golem-tui-bin",
	})
	rs := newReleaseServer(t, tarball)

	out, paths := runUpdate(t, rs, "v0.1.0")

	for _, path := range paths {
		body, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("read %s: %v", path, err)
		}

		if !strings.Contains(string(body), "new-golem") {
			t.Errorf("%s content = %q", path, string(body))
		}

		info, _ := os.Stat(path)
		if perm := info.Mode().Perm(); perm != 0o755 {
			t.Errorf("%s perms = %o, want 755", path, perm)
		}
	}

	for _, want := range []string{"checksum verified", "installed golem, golem-tui", "v9.9.9"} {
		if !strings.Contains(out, want) {
			t.Errorf("output missing %q:\n%s", want, out)
		}
	}
}

func TestRunUpdate_AlreadyUpToDateSkipsDownload(t *testing.T) {
	t.Parallel()

	tarball := buildTarGz(t, map[string]string{"golem": "x", "golem-tui": "y"})
	rs := newReleaseServer(t, tarball)

	var out strings.Builder

	dir := t.TempDir()

	err := cli.RunUpdate(cli.UpdateOptions{
		Stdout:  &out,
		Current: "v9.9.9",
		APIURL:  rs.srv.URL + "/api",
		GOOS:    "linux",
		GOARCH:  "amd64",
		InstallDir: func() (string, error) {
			return dir, nil
		},
	})
	if err != nil {
		t.Fatalf("RunUpdate: %v", err)
	}

	if !strings.Contains(out.String(), "up to date") {
		t.Errorf("expected up-to-date notice, got:\n%s", out.String())
	}

	if rs.apiHits != 1 {
		t.Errorf("api hits = %d, want 1 (no asset downloads)", rs.apiHits)
	}

	if _, statErr := os.Stat(filepath.Join(dir, "golem")); !os.IsNotExist(statErr) {
		t.Error("no files should be installed when up to date")
	}
}

func TestRunUpdate_DevSnapshotProceeds(t *testing.T) {
	t.Parallel()

	tarball := buildTarGz(t, map[string]string{"golem": "dev-update", "golem-tui": "tui"})
	rs := newReleaseServer(t, tarball)

	out, _ := runUpdate(t, rs, "dev")

	if !strings.Contains(out, "dev → v9.9.9") {
		t.Errorf("dev builds must update unconditionally, got:\n%s", out)
	}
}

func TestRunUpdate_GitDescribeSnapshotProceeds(t *testing.T) {
	t.Parallel()

	tarball := buildTarGz(t, map[string]string{"golem": "snapshot", "golem-tui": "tui"})
	rs := newReleaseServer(t, tarball)

	out, _ := runUpdate(t, rs, "v0.1.2-5-gabc1234")

	if !strings.Contains(out, "v9.9.9") {
		t.Errorf("snapshot builds must update, got:\n%s", out)
	}
}

func TestRunUpdate_ChecksumMismatchRefusesInstall(t *testing.T) {
	t.Parallel()

	tarball := buildTarGz(t, map[string]string{"golem": "evil", "golem-tui": "tui"})
	rs := newReleaseServer(t, tarball)
	rs.corrupt = true

	var out strings.Builder

	dir := t.TempDir()

	err := cli.RunUpdate(cli.UpdateOptions{
		Stdout:  &out,
		Current: "v0.1.0",
		APIURL:  rs.srv.URL + "/api",
		GOOS:    "linux",
		GOARCH:  "amd64",
		InstallDir: func() (string, error) {
			return dir, nil
		},
	})

	if err == nil || !strings.Contains(err.Error(), "checksum mismatch") {
		t.Fatalf("expected checksum mismatch, got: %v", err)
	}

	if _, statErr := os.Stat(filepath.Join(dir, "golem")); !os.IsNotExist(statErr) {
		t.Error("mismatched checksums must not install anything")
	}
}

func TestRunUpdate_MissingPlatformAssetFails(t *testing.T) {
	t.Parallel()

	tarball := buildTarGz(t, map[string]string{"golem": "x", "golem-tui": "y"})
	rs := newReleaseServer(t, tarball)

	var out strings.Builder

	err := cli.RunUpdate(cli.UpdateOptions{
		Stdout:  &out,
		Current: "v0.1.0",
		APIURL:  rs.srv.URL + "/api",
		GOOS:    "plan9",
		GOARCH:  "amd64",
		InstallDir: func() (string, error) {
			return t.TempDir(), nil
		},
	})

	if err == nil || !strings.Contains(err.Error(), "no release asset") {
		t.Fatalf("expected missing-asset error, got: %v", err)
	}
}
