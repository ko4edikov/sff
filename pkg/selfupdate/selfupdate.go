// Package selfupdate replaces the running sff binary with a release published on
// GitHub (the same archives the install scripts download).
package selfupdate

import (
	"archive/tar"
	"archive/zip"
	"bufio"
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
	"runtime"
	"strconv"
	"strings"
	"time"
)

// Updater talks to a GitHub repository's releases.
type Updater struct {
	// Repo is "owner/name".
	Repo string
	// APIBase and DownloadBase default to the public GitHub endpoints; tests override them.
	APIBase      string
	DownloadBase string
	HTTP         *http.Client
	// GOOS and GOARCH default to the running platform.
	GOOS, GOARCH string
}

// New returns an Updater for the given "owner/name" repository.
func New(repo string) *Updater {
	return &Updater{
		Repo:         repo,
		APIBase:      "https://api.github.com",
		DownloadBase: "https://github.com",
		HTTP:         &http.Client{Timeout: 5 * time.Minute},
		GOOS:         runtime.GOOS,
		GOARCH:       runtime.GOARCH,
	}
}

// LatestTag returns the tag (e.g. "v0.9.0") of the latest non-prerelease release.
func (u *Updater) LatestTag(ctx context.Context) (string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet,
		fmt.Sprintf("%s/repos/%s/releases/latest", u.APIBase, u.Repo), nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	resp, err := u.HTTP.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("looking up the latest release: HTTP %d", resp.StatusCode)
	}
	var body struct {
		TagName string `json:"tag_name"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		return "", err
	}
	if body.TagName == "" {
		return "", errors.New("latest release has no tag")
	}
	return body.TagName, nil
}

// archiveName mirrors the goreleaser name_template.
func (u *Updater) archiveName(tag string) string {
	ext := "tar.gz"
	if u.GOOS == "windows" {
		ext = "zip"
	}
	return fmt.Sprintf("sff_%s_%s_%s.%s", strings.TrimPrefix(tag, "v"), u.GOOS, u.GOARCH, ext)
}

func (u *Updater) get(ctx context.Context, url string) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	resp, err := u.HTTP.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("GET %s: HTTP %d", url, resp.StatusCode)
	}
	return io.ReadAll(resp.Body)
}

// Download fetches the release archive for tag, verifies it against the
// release's checksums.txt and returns the extracted sff binary.
func (u *Updater) Download(ctx context.Context, tag string) ([]byte, error) {
	base := fmt.Sprintf("%s/%s/releases/download/%s", u.DownloadBase, u.Repo, tag)
	name := u.archiveName(tag)
	archive, err := u.get(ctx, base+"/"+name)
	if err != nil {
		return nil, err
	}
	sums, err := u.get(ctx, base+"/checksums.txt")
	if err != nil {
		return nil, fmt.Errorf("fetching checksums: %w", err)
	}
	if err := verifyChecksum(archive, name, sums); err != nil {
		return nil, err
	}
	return extractBinary(archive, u.GOOS == "windows")
}

func verifyChecksum(data []byte, name string, sums []byte) error {
	sc := bufio.NewScanner(bytes.NewReader(sums))
	for sc.Scan() {
		f := strings.Fields(sc.Text())
		if len(f) == 2 && f[1] == name {
			got := sha256.Sum256(data)
			if !strings.EqualFold(hex.EncodeToString(got[:]), f[0]) {
				return fmt.Errorf("checksum mismatch for %s", name)
			}
			return nil
		}
	}
	return fmt.Errorf("no checksum listed for %s", name)
}

func extractBinary(archive []byte, isZip bool) ([]byte, error) {
	if isZip {
		zr, err := zip.NewReader(bytes.NewReader(archive), int64(len(archive)))
		if err != nil {
			return nil, err
		}
		for _, f := range zr.File {
			if filepath.Base(f.Name) == "sff.exe" {
				rc, err := f.Open()
				if err != nil {
					return nil, err
				}
				defer rc.Close()
				return io.ReadAll(rc)
			}
		}
		return nil, errors.New("sff.exe not found in archive")
	}
	gz, err := gzip.NewReader(bytes.NewReader(archive))
	if err != nil {
		return nil, err
	}
	tr := tar.NewReader(gz)
	for {
		h, err := tr.Next()
		if err == io.EOF {
			return nil, errors.New("sff not found in archive")
		}
		if err != nil {
			return nil, err
		}
		if h.Typeflag == tar.TypeReg && filepath.Base(h.Name) == "sff" {
			return io.ReadAll(tr)
		}
	}
}

// Replace overwrites the executable at path with bin. The new file is written
// next to it and renamed into place; the old file is moved aside first so this
// also works on Windows, where a running exe can be renamed but not overwritten.
func Replace(path string, bin []byte) error {
	dir := filepath.Dir(path)
	tmp, err := os.CreateTemp(dir, ".sff-update-*")
	if err != nil {
		return fmt.Errorf("cannot write to %s (try running with sudo?): %w", dir, err)
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName)
	if _, err := tmp.Write(bin); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Chmod(tmpName, 0o755); err != nil {
		return err
	}
	if runtime.GOOS == "windows" {
		old := path + ".old"
		os.Remove(old)
		if err := os.Rename(path, old); err != nil {
			return err
		}
		if err := os.Rename(tmpName, path); err != nil {
			os.Rename(old, path)
			return err
		}
		return nil
	}
	return os.Rename(tmpName, path)
}

// Compare returns -1, 0 or 1 comparing semver-ish versions ("v0.9.0", "0.10.1").
// ok is false when either side isn't a plain release version (e.g. "dev" or a
// "v0.9.0-3-gabc" git-describe build).
func Compare(a, b string) (cmp int, ok bool) {
	pa, oka := parse(a)
	pb, okb := parse(b)
	if !oka || !okb {
		return 0, false
	}
	for i := range pa {
		if pa[i] != pb[i] {
			if pa[i] < pb[i] {
				return -1, true
			}
			return 1, true
		}
	}
	return 0, true
}

func parse(v string) ([3]int, bool) {
	var out [3]int
	parts := strings.Split(strings.TrimPrefix(v, "v"), ".")
	if len(parts) != 3 {
		return out, false
	}
	for i, p := range parts {
		n, err := strconv.Atoi(p)
		if err != nil {
			return out, false
		}
		out[i] = n
	}
	return out, true
}
