package source

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/ko4edikov/sff/pkg/mdapi"
	"github.com/ko4edikov/sff/pkg/project"
)

// ResolveRetrieve builds a Retrieve target from a local source path, deciding
// what to pull from the org via the Metadata API and how to narrow the diff.
//
// It mirrors IC2's behavior for decomposed metadata: a child file (e.g. a custom
// field) resolves to its parent type retrieved whole (CustomObject), with the
// comparison scoped back down to the one file. A component directory diffs the
// whole subtree; a non-decomposed file diffs that single file. The metadata type
// for non-decomposed paths comes from the describe catalog (directoryName→type).
func ResolveRetrieve(path string, proj *project.Project, catalog *mdapi.DescribeResult) (*Target, error) {
	rel, err := sourceRelOf(path, proj)
	if err != nil {
		return nil, err
	}
	parts := strings.Split(rel, "/")
	if len(parts) < 2 {
		return nil, fmt.Errorf("%s: point at a component or file, not the whole %q directory", path, rel)
	}
	folder := parts[0]

	isDir := false
	if fi, err := os.Stat(path); err == nil {
		isDir = fi.IsDir()
	}

	// Decomposed family (objects, objectTranslations, bots): retrieve the parent
	// type whole. parts[1] is always the parent component's folder name.
	if t := decompByDir[folder]; t != nil {
		member := parts[1]
		tgt := &Target{
			Kind:           Retrieve,
			Name:           t.Name + ":" + member,
			LocalPath:      path,
			RetrieveType:   t.Name,
			RetrieveMember: member,
			Project:        proj,
		}
		if !isDir {
			tgt.ScopeRel = rel // a single file inside the component
		}
		return tgt, nil
	}

	// Non-decomposed type: map the directory to its Metadata API type via the
	// describe catalog, and retrieve the single named component.
	obj, ok := catalogByDir(catalog)[folder]
	if !ok {
		return nil, fmt.Errorf("%s: unknown metadata directory %q (no matching type in org describe)", path, folder)
	}
	if isDir {
		return nil, fmt.Errorf("%s: %s is not a decomposed type; point at a file", path, obj.Name)
	}
	member := memberFromFile(parts[len(parts)-1], obj.Suffix)
	return &Target{
		Kind:           Retrieve,
		Name:           obj.Name + ":" + member,
		LocalPath:      path,
		RetrieveType:   obj.Name,
		RetrieveMember: member,
		ScopeRel:       rel,
		Project:        proj,
	}, nil
}

// memberFromFile derives a metadata member name from a source file name by
// stripping the "-meta.xml" wrapper and the type suffix (e.g.
// "Account-Account Layout.layout-meta.xml" → "Account-Account Layout").
func memberFromFile(name, suffix string) string {
	name = strings.TrimSuffix(name, "-meta.xml")
	if suffix != "" {
		name = strings.TrimSuffix(name, "."+suffix)
	} else if ext := filepath.Ext(name); ext != "" {
		name = strings.TrimSuffix(name, ext)
	}
	return name
}

// sourceRelOf returns path's forward-slash location relative to the package
// directory it lives in, with any leading "main/default/" stripped, so the first
// segment is the metadata directory name (e.g. "objects", "layouts").
func sourceRelOf(path string, proj *project.Project) (string, error) {
	abs, err := filepath.Abs(path)
	if err != nil {
		return "", err
	}
	for _, dir := range proj.AbsDirs() {
		rel, err := filepath.Rel(dir, abs)
		if err != nil || strings.HasPrefix(rel, "..") {
			continue
		}
		rel = filepath.ToSlash(rel)
		rel = strings.TrimPrefix(rel, "main/default/")
		return rel, nil
	}
	return "", fmt.Errorf("%s is not under a package directory of %s", path, proj.Root)
}
