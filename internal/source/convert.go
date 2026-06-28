package source

import (
	"archive/zip"
	"bytes"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/ko4edikov/sff/internal/project"
)

// contentFolders hold metadata whose files are byte-identical between the
// metadata and source formats (a content file plus its "-meta.xml"); they
// convert by a verbatim copy.
var contentFolders = map[string]bool{
	"classes":    true,
	"triggers":   true,
	"pages":      true,
	"components": true,
	"lwc":        true,
	"aura":       true,
}

// decomposedFolders need work the source converter doesn't do yet: splitting a
// composed XML into many child files (objects, workflows, …) or remapping a
// binary's extension from its content type (staticresources). Entries here are
// skipped with a warning rather than written incorrectly.
var decomposedFolders = map[string]bool{
	"objects":         true,
	"staticresources": true,
	"workflows":       true,
	"sharingRules":    true,
	"bots":            true,
}

// ConvertResult reports what a metadata→source conversion produced.
type ConvertResult struct {
	Written  []string // source-relative paths written
	Warnings []string // components skipped or needing attention
}

// ConvertZipToSource extracts a metadata-format retrieve zip (singlePackage,
// so entries are like "classes/Foo.cls") and writes it into the sfdx project p
// as source format: content types are copied verbatim, XML-only types get a
// "-meta.xml" suffix, and decomposed types are skipped with a warning. Existing
// files are overwritten in place; new files land under the default package
// directory.
func ConvertZipToSource(zipBytes []byte, p *project.Project) (*ConvertResult, error) {
	zr, err := zip.NewReader(bytes.NewReader(zipBytes), int64(len(zipBytes)))
	if err != nil {
		return nil, fmt.Errorf("open retrieved zip: %w", err)
	}

	res := &ConvertResult{}
	warnedFolder := map[string]bool{}
	for _, f := range zr.File {
		if f.FileInfo().IsDir() {
			continue
		}
		name := filepath.ToSlash(f.Name)
		if name == "package.xml" {
			continue
		}
		folder := topFolder(name)
		if decomposedFolders[folder] {
			if !warnedFolder[folder] {
				warnedFolder[folder] = true
				res.Warnings = append(res.Warnings, fmt.Sprintf(
					"%s: source-format conversion not supported yet — skipped (use --metadata-format for raw output)", folder))
			}
			continue
		}

		srcRel := sourceRel(name, folder)
		target := placeInProject(p, srcRel)
		data, err := readZipEntry(f)
		if err != nil {
			return res, err
		}
		if err := writeFile(target, data); err != nil {
			return res, err
		}
		res.Written = append(res.Written, srcRel)
	}
	return res, nil
}

// topFolder returns the first path segment of a forward-slash path.
func topFolder(name string) string {
	if i := strings.IndexByte(name, '/'); i >= 0 {
		return name[:i]
	}
	return name
}

// sourceRel maps a metadata-format path to its source-format equivalent. Content
// types are unchanged; XML-only types gain a "-meta.xml" suffix on the bare
// type file.
func sourceRel(name, folder string) string {
	if contentFolders[folder] {
		return name
	}
	if strings.HasSuffix(name, "-meta.xml") {
		return name
	}
	return name + "-meta.xml"
}

// placeInProject returns the absolute path to write srcRel to: an existing file
// in any package directory (overwritten in place), else a new file under the
// default package directory's main/default tree.
func placeInProject(p *project.Project, srcRel string) string {
	rel := filepath.FromSlash(srcRel)
	for _, d := range p.AbsDirs() {
		for _, cand := range []string{
			filepath.Join(d, "main", "default", rel),
			filepath.Join(d, rel),
		} {
			if _, err := os.Stat(cand); err == nil {
				return cand
			}
		}
	}
	base := p.DefaultDir()
	if fi, err := os.Stat(filepath.Join(base, "main", "default")); err == nil && fi.IsDir() {
		return filepath.Join(base, "main", "default", rel)
	}
	return filepath.Join(base, rel)
}

func readZipEntry(f *zip.File) ([]byte, error) {
	rc, err := f.Open()
	if err != nil {
		return nil, fmt.Errorf("read zip entry %s: %w", f.Name, err)
	}
	defer rc.Close()
	return io.ReadAll(rc)
}

func writeFile(path string, data []byte) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	return os.WriteFile(path, data, 0o644)
}
