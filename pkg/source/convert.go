package source

import (
	"archive/zip"
	"bytes"
	"fmt"
	"io"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/ko4edikov/sff/pkg/mdapi"
	"github.com/ko4edikov/sff/pkg/project"
)

// contentFolders is the fallback set of metadata whose files copy verbatim
// between metadata and source format. It is only consulted when no describe
// catalog is available; otherwise classification is data-driven (see classify).
var contentFolders = map[string]bool{
	"classes":    true,
	"triggers":   true,
	"pages":      true,
	"components": true,
	"lwc":        true,
	"aura":       true,
}

// ConvertResult reports what a metadata→source conversion produced.
type ConvertResult struct {
	Written  []string // source-relative paths written
	Warnings []string // components skipped or needing attention
}

// ConvertZipToSource extracts a metadata-format retrieve zip (singlePackage, so
// entries are like "classes/Foo.cls") and writes it into the sfdx project p as
// source format. catalog, when non-nil, drives content/XML-only classification
// via each type's metaFile/suffix; decomposed types (objects, …) are split per
// the embedded decomposition table. Existing files are overwritten in place;
// new files land under the default package directory.
func ConvertZipToSource(zipBytes []byte, p *project.Project, catalog *mdapi.DescribeResult) (*ConvertResult, error) {
	zr, err := zip.NewReader(bytes.NewReader(zipBytes), int64(len(zipBytes)))
	if err != nil {
		return nil, fmt.Errorf("open retrieved zip: %w", err)
	}

	byDir := catalogByDir(catalog)
	srContentTypes := staticResourceContentTypes(zr)
	res := &ConvertResult{}
	for _, f := range zr.File {
		if f.FileInfo().IsDir() {
			continue
		}
		name := filepath.ToSlash(f.Name)
		if name == "package.xml" {
			continue
		}
		folder := topFolder(name)

		data, err := readZipEntry(f)
		if err != nil {
			return res, err
		}

		// Static resources: the .resource-meta.xml copies across (re-serialized
		// like other XML), while the .resource binary is renamed by content type
		// or, for archives, expanded into a directory.
		if folder == "staticresources" {
			if strings.HasSuffix(name, ".resource-meta.xml") {
				if err := writeFile(placeInProject(p, name), normalizeXML(data)); err != nil {
					return res, err
				}
				res.Written = append(res.Written, name)
				continue
			}
			ct := srContentTypes[strings.TrimSuffix(name, ".resource")]
			parts, err := convertStaticResource(name, data, ct)
			if err != nil {
				return res, fmt.Errorf("static resource %s: %w", name, err)
			}
			for _, sp := range parts {
				if err := writeFile(placeInProject(p, sp.rel), sp.data); err != nil {
					return res, err
				}
				res.Written = append(res.Written, sp.rel)
			}
			continue
		}

		// Decomposed types expand one composed file into many source files.
		if t := decompByDir[folder]; t != nil {
			parentName := strings.TrimSuffix(path.Base(name), "."+t.Suffix)
			parts, err := decompose(data, parentName, t)
			if err != nil {
				return res, fmt.Errorf("decompose %s: %w", name, err)
			}
			for _, sp := range parts {
				if err := writeFile(placeInProject(p, sp.rel), normalizeXML(sp.data)); err != nil {
					return res, err
				}
				res.Written = append(res.Written, sp.rel)
			}
			continue
		}

		srcRel := sourceRel(name, folder, byDir)
		// XML-only types are re-serialized by sf (CRLF→LF, empty tags expanded);
		// match that so re-retrieving doesn't churn sf-created files. Verbatim
		// content/bundles are copied byte-for-byte.
		if !isVerbatim(folder, byDir) {
			data = normalizeXML(data)
		}
		if err := writeFile(placeInProject(p, srcRel), data); err != nil {
			return res, err
		}
		res.Written = append(res.Written, srcRel)
	}
	return res, nil
}

// catalogByDir indexes a describe catalog by directoryName, or returns nil.
func catalogByDir(catalog *mdapi.DescribeResult) map[string]mdapi.MetadataObject {
	if catalog == nil {
		return nil
	}
	m := make(map[string]mdapi.MetadataObject, len(catalog.Objects))
	for _, o := range catalog.Objects {
		m[o.DirectoryName] = o
	}
	return m
}

// topFolder returns the first path segment of a forward-slash path.
func topFolder(name string) string {
	if i := strings.IndexByte(name, '/'); i >= 0 {
		return name[:i]
	}
	return name
}

// sourceRel maps a metadata-format path to its source-format equivalent. Content
// types (a content file plus its -meta.xml) and bundles copy verbatim; XML-only
// types gain a "-meta.xml" suffix on the bare type file. Classification prefers
// the describe catalog (metaFile/suffix) and falls back to contentFolders.
func sourceRel(name, folder string, byDir map[string]mdapi.MetadataObject) string {
	if strings.HasSuffix(name, "-meta.xml") {
		return name // already a source meta file (e.g. inside a bundle)
	}
	if isVerbatim(folder, byDir) {
		return name
	}
	return name + "-meta.xml"
}

// isVerbatim reports whether a folder's files copy across unchanged. With a
// catalog: bundles (empty suffix) and content types (metaFile=true) are
// verbatim, XML-only types (metaFile=false) are not. Without a catalog, it falls
// back to the contentFolders allowlist.
func isVerbatim(folder string, byDir map[string]mdapi.MetadataObject) bool {
	if byDir != nil {
		if o, ok := byDir[folder]; ok {
			return o.Suffix == "" || o.MetaFile
		}
	}
	return contentFolders[folder]
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

// emptyTagRe matches an empty self-closing element, optionally with attributes.
var emptyTagRe = regexp.MustCompile(`<([\w:.-]+)([^<>]*?)\s*/>`)

// normalizeXML matches the Salesforce source serializer for XML metadata:
// line endings become LF and empty self-closing elements are expanded to an
// open/close pair (<x/> → <x></x>). This keeps sff-converted files byte-aligned
// with sf-created ones.
func normalizeXML(data []byte) []byte {
	s := strings.ReplaceAll(string(data), "\r\n", "\n")
	s = strings.ReplaceAll(s, "\r", "\n")
	s = emptyTagRe.ReplaceAllString(s, "<$1$2></$1>")
	return []byte(s)
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
