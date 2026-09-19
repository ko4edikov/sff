package source

import (
	"io/fs"
	"path/filepath"
	"strings"

	"github.com/ko4edikov/sff/pkg/mdapi"
)

// BuildPlacementIndex walks every directory in dirs once and maps each existing
// file's type-relative suffix (the same "srcRel" strings convert.go already
// produces, e.g. "classes/Foo.cls-meta.xml" or
// "objects/Account/fields/Bar__c.field-meta.xml") to its absolute path.
//
// This lets placeInProject find a component's existing file no matter how
// deep a project nests its package directory — e.g. a custom feature-module
// layout like "authorization/src/classes/Foo.cls-meta.xml" — matching how
// `sf project retrieve start` locates local components anywhere in the tree
// instead of only directly under the package root or its main/default.
func BuildPlacementIndex(dirs []string, catalog *mdapi.DescribeResult) map[string]string {
	typeDirs := knownTypeDirs(catalog)
	index := make(map[string]string)
	for _, root := range dirs {
		filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
			if err != nil || d.IsDir() {
				return nil
			}
			rel := relFromTypeDir(root, p, typeDirs)
			if rel == "" {
				return nil
			}
			if _, exists := index[rel]; !exists {
				index[rel] = p
			}
			return nil
		})
	}
	return index
}

// knownTypeDirs is the set of source directory names that mark the start of a
// component's type-relative path (e.g. "classes", "objects"): every
// directoryName from the describe catalog when available, plus the
// decomposition table's directories and the no-catalog fallback map, so
// placement still works when describe failed or wasn't fetched.
func knownTypeDirs(catalog *mdapi.DescribeResult) map[string]bool {
	dirs := make(map[string]bool, len(dirToType)+len(decompByDir))
	for dir := range dirToType {
		dirs[dir] = true
	}
	for dir := range decompByDir {
		dirs[dir] = true
	}
	if catalog != nil {
		for _, o := range catalog.Objects {
			dirs[o.DirectoryName] = true
		}
	}
	return dirs
}

// relFromTypeDir returns p's slash-separated suffix (relative to root)
// starting at the outermost path segment that names a known metadata type
// directory, or "" if none matches. The outermost match is used so a
// component nested arbitrarily deep under custom module folders still keys
// on e.g. "classes/...", matching the srcRel format placeInProject expects.
func relFromTypeDir(root, p string, typeDirs map[string]bool) string {
	relPath, err := filepath.Rel(root, p)
	if err != nil {
		return ""
	}
	parts := strings.Split(filepath.ToSlash(relPath), "/")
	for i, part := range parts {
		if typeDirs[part] {
			return strings.Join(parts[i:], "/")
		}
	}
	return ""
}
