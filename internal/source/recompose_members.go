package source

import (
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"github.com/ko4edikov/sff/internal/mdapi"
	"github.com/ko4edikov/sff/internal/project"
)

// typeToDirFallback reverses dirToType, mapping a Metadata API type back to its
// source directory when no describe catalog is available.
var typeToDirFallback = func() map[string]string {
	m := make(map[string]string, len(dirToType))
	for dir, typ := range dirToType {
		m[typ] = dir
	}
	return m
}()

// decompByName indexes the decomposition table by Metadata API type name.
var decompByName = func() map[string]*DecompType {
	m := make(map[string]*DecompType, len(decompByDir))
	for _, t := range decompByDir {
		m[t.Name] = t
	}
	return m
}()

// memberFile is a resolved source file plus its metadata-relative path.
type memberFile struct {
	metaRel string
	data    []byte
}

// byTypeName indexes a describe catalog by xmlName, or returns nil.
func byTypeName(catalog *mdapi.DescribeResult) map[string]mdapi.MetadataObject {
	if catalog == nil {
		return nil
	}
	m := make(map[string]mdapi.MetadataObject, len(catalog.Objects))
	for _, o := range catalog.Objects {
		m[o.Name] = o
	}
	return m
}

// memberRoots lists the directories that directly contain metadata type folders:
// each package directory's main/default tree and the package directory itself.
func memberRoots(proj *project.Project) []string {
	var roots []string
	for _, d := range proj.AbsDirs() {
		roots = append(roots, filepath.Join(d, "main", "default"), d)
	}
	return roots
}

// resolveMemberFiles finds the source files for one (type, member) under the
// first package root that contains them. A "*" member selects every component of
// the type. Returns nil (no error) when nothing local matches.
func resolveMemberFiles(roots []string, typeName, member string, byType map[string]mdapi.MetadataObject) ([]memberFile, error) {
	dir, suffix, verbatim, container := classifyType(typeName, byType)
	if dir == "" {
		return nil, nil // unknown type; the caller reports it as not found
	}

	for _, root := range roots {
		typeDir := filepath.Join(root, filepath.FromSlash(dir))
		var paths []string
		switch {
		case member == "*":
			paths = walkFiles(typeDir)
		case container:
			paths = walkFiles(filepath.Join(typeDir, filepath.FromSlash(member)))
		case typeName == "StaticResource":
			paths = staticResourcePaths(typeDir, member)
		default:
			paths = flatMemberPaths(typeDir, member, suffix, verbatim)
		}
		if len(paths) == 0 {
			continue
		}
		return readMemberFiles(root, paths)
	}
	return nil, nil
}

// classifyType resolves a type's source directory and layout. container is true
// for decomposed types and bundles (whole-subtree components); verbatim marks
// content types whose files copy across unchanged.
func classifyType(typeName string, byType map[string]mdapi.MetadataObject) (dir, suffix string, verbatim, container bool) {
	if t := decompByName[typeName]; t != nil {
		return t.DirectoryName, t.Suffix, false, true
	}
	if byType != nil {
		if o, ok := byType[typeName]; ok {
			bundle := o.Suffix == ""
			return o.DirectoryName, o.Suffix, o.MetaFile || bundle, bundle
		}
	}
	dir = typeToDirFallback[typeName]
	if dir == "" {
		return "", "", false, false
	}
	bundle := bundleDirs[dir]
	return dir, "", contentFolders[dir] || bundle, bundle
}

// flatMemberPaths returns the source file(s) for a single flat component: the
// content file and/or its -meta.xml sidecar. member may carry a folder prefix
// for in-folder types. When the suffix is unknown it globs "<member>.*".
func flatMemberPaths(typeDir, member, suffix string, verbatim bool) []string {
	rel := filepath.FromSlash(member)
	var paths []string
	if suffix != "" {
		content := filepath.Join(typeDir, rel+"."+suffix)
		meta := content + "-meta.xml"
		if verbatim {
			paths = appendIfFile(paths, content)
			paths = appendIfFile(paths, meta)
		} else {
			paths = appendIfFile(paths, meta)
			if len(paths) == 0 {
				paths = appendIfFile(paths, content)
			}
		}
		if len(paths) > 0 {
			return paths
		}
	}
	matches, _ := filepath.Glob(filepath.Join(typeDir, rel+".*"))
	for _, m := range matches {
		paths = appendIfFile(paths, m)
	}
	return paths
}

// staticResourcePaths returns a static resource's files: its .resource-meta.xml
// plus either the expanded archive directory or the single content file.
func staticResourcePaths(srDir, member string) []string {
	var paths []string
	paths = appendIfFile(paths, filepath.Join(srDir, member+".resource-meta.xml"))

	if archive := filepath.Join(srDir, member); isDir(archive) {
		return append(paths, walkFiles(archive)...)
	}
	matches, _ := filepath.Glob(filepath.Join(srDir, member+".*"))
	for _, m := range matches {
		if strings.HasSuffix(m, ".resource-meta.xml") {
			continue
		}
		paths = appendIfFile(paths, m)
	}
	return paths
}

// readMemberFiles reads each path and pairs it with its path relative to root,
// de-duplicating repeats.
func readMemberFiles(root string, paths []string) ([]memberFile, error) {
	seen := map[string]bool{}
	var out []memberFile
	for _, p := range paths {
		rel, err := filepath.Rel(root, p)
		if err != nil {
			return nil, err
		}
		mr := filepath.ToSlash(rel)
		if seen[mr] {
			continue
		}
		seen[mr] = true
		data, err := os.ReadFile(p)
		if err != nil {
			return nil, err
		}
		out = append(out, memberFile{metaRel: mr, data: data})
	}
	return out, nil
}

// walkFiles returns every regular file under dir (recursively), or nil if dir
// doesn't exist.
func walkFiles(dir string) []string {
	var out []string
	_ = filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
		if err == nil && !d.IsDir() {
			out = append(out, p)
		}
		return nil
	})
	return out
}

func appendIfFile(paths []string, p string) []string {
	if isFile(p) {
		return append(paths, p)
	}
	return paths
}

func isFile(p string) bool {
	fi, err := os.Stat(p)
	return err == nil && !fi.IsDir()
}

func isDir(p string) bool {
	fi, err := os.Stat(p)
	return err == nil && fi.IsDir()
}
