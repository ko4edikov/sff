package mdapi

import (
	"archive/zip"
	"bytes"
	"encoding/xml"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// BuildZip packs metadata-format entries (forward-slash paths to their bytes,
// including package.xml) into an in-memory zip suitable for a singlePackage
// deploy. Entries are written in sorted order so the archive is deterministic.
func BuildZip(entries map[string][]byte) ([]byte, error) {
	names := make([]string, 0, len(entries))
	for name := range entries {
		names = append(names, name)
	}
	sort.Strings(names)

	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	for _, name := range names {
		w, err := zw.Create(name)
		if err != nil {
			return nil, fmt.Errorf("zip entry %s: %w", name, err)
		}
		if _, err := w.Write(entries[name]); err != nil {
			return nil, fmt.Errorf("write zip entry %s: %w", name, err)
		}
	}
	if err := zw.Close(); err != nil {
		return nil, fmt.Errorf("finalize zip: %w", err)
	}
	return buf.Bytes(), nil
}

// LoadMetadataDir reads an already-in-metadata-format directory (the kind sff
// retrieve --metadata-format writes) into zip entries plus the parsed manifest.
// Every file is included verbatim under its forward-slash path relative to dir,
// and a package.xml at the root is required — it is both kept as an entry and
// returned as the Package for dry-run display and counting.
func LoadMetadataDir(dir string) (map[string][]byte, *Package, error) {
	info, err := os.Stat(dir)
	if err != nil {
		return nil, nil, err
	}
	if !info.IsDir() {
		return nil, nil, fmt.Errorf("%s is not a directory", dir)
	}

	entries := make(map[string][]byte)
	err = filepath.WalkDir(dir, func(p string, d os.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		rel, rerr := filepath.Rel(dir, p)
		if rerr != nil {
			return rerr
		}
		data, derr := os.ReadFile(p)
		if derr != nil {
			return fmt.Errorf("read %s: %w", rel, derr)
		}
		entries[filepath.ToSlash(rel)] = data
		return nil
	})
	if err != nil {
		return nil, nil, err
	}

	manifest, ok := entries["package.xml"]
	if !ok {
		return nil, nil, fmt.Errorf("no package.xml at the root of %s (not a metadata-format directory)", dir)
	}
	var pkg Package
	if err := xml.Unmarshal(manifest, &pkg); err != nil {
		return nil, nil, fmt.Errorf("parse package.xml: %w", err)
	}
	pkg.Xmlns = metadataNS
	return entries, &pkg, nil
}

// Unzip extracts a zip archive (the bytes returned by a retrieve) into destDir,
// returning the relative paths written. It rejects entries that would escape
// destDir (zip-slip).
func Unzip(data []byte, destDir string) ([]string, error) {
	zr, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		return nil, fmt.Errorf("open retrieved zip: %w", err)
	}
	if err := os.MkdirAll(destDir, 0o755); err != nil {
		return nil, fmt.Errorf("create %s: %w", destDir, err)
	}

	// Resolve to an absolute path so the zip-slip guard works for relative
	// dests like "." (where filepath.Clean would leave a bare ".").
	cleanDest, err := filepath.Abs(destDir)
	if err != nil {
		return nil, err
	}
	var written []string
	for _, f := range zr.File {
		target := filepath.Join(cleanDest, filepath.FromSlash(f.Name))
		// Guard against path traversal.
		if target != cleanDest && !strings.HasPrefix(target, cleanDest+string(os.PathSeparator)) {
			return nil, fmt.Errorf("zip entry %q escapes output dir", f.Name)
		}
		if f.FileInfo().IsDir() {
			if err := os.MkdirAll(target, 0o755); err != nil {
				return nil, err
			}
			continue
		}
		if err := writeZipFile(f, target); err != nil {
			return nil, err
		}
		written = append(written, f.Name)
	}
	return written, nil
}

func writeZipFile(f *zip.File, target string) error {
	if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
		return err
	}
	rc, err := f.Open()
	if err != nil {
		return fmt.Errorf("read zip entry %s: %w", f.Name, err)
	}
	defer rc.Close()

	out, err := os.OpenFile(target, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o644)
	if err != nil {
		return fmt.Errorf("create %s: %w", target, err)
	}
	defer out.Close()

	if _, err := io.Copy(out, rc); err != nil {
		return fmt.Errorf("write %s: %w", target, err)
	}
	return nil
}
