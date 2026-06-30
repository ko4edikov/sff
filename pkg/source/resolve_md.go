package source

import (
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"github.com/ko4edikov/sff/pkg/mdapi"
	"github.com/ko4edikov/sff/pkg/project"
)

// ResolveRetrieve builds a Retrieve target from a local source path, deciding
// what to pull from the org via the Metadata API and how to narrow the diff.
//
// A single file resolves to one component (a decomposed child like a custom
// field maps to its parent type retrieved whole, e.g. CustomObject). A directory
// is walked and every component beneath it is collected into one bulk retrieve —
// so a single component dir (objects/Account), a whole type dir (objects/,
// layouts/), or any broader directory (force-app/main/default) all work. ScopeRel
// narrows the comparison back down to what was pointed at.
func ResolveRetrieve(path string, proj *project.Project, catalog *mdapi.DescribeResult) (*Target, error) {
	rel, err := sourceRelOf(path, proj)
	if err != nil {
		return nil, err
	}
	byDir := catalogByDir(catalog)

	fi, statErr := os.Stat(path)
	isDir := statErr == nil && fi.IsDir()

	if !isDir {
		spec, ok := relToSpec(rel, byDir)
		if !ok {
			return nil, fmt.Errorf("%s: unsupported metadata file", path)
		}
		return &Target{
			Kind:          Retrieve,
			Name:          spec,
			LocalPath:     path,
			RetrieveSpecs: []string{spec},
			ScopeRel:      rel,
			Project:       proj,
		}, nil
	}

	specs, err := enumerateSpecs(path, proj, byDir)
	if err != nil {
		return nil, err
	}
	if len(specs) == 0 {
		return nil, fmt.Errorf("%s: no retrievable metadata under this directory", path)
	}
	name := rel
	if name == "" {
		name = "all"
	}
	if len(specs) > 1 {
		name = fmt.Sprintf("%s (%d components)", name, len(specs))
	}
	return &Target{
		Kind:          Retrieve,
		Name:          name,
		LocalPath:     path,
		RetrieveSpecs: specs,
		ScopeRel:      rel,
		Project:       proj,
	}, nil
}

// relToSpec maps a source-relative file path to a Metadata API "Type:Member"
// specifier. A decomposed child (objects/Account/fields/X__c.field-meta.xml)
// maps to its parent component (CustomObject:Account); a non-decomposed file
// maps to its own type via the describe catalog. ok is false for paths that
// don't correspond to a metadata component.
func relToSpec(rel string, byDir map[string]mdapi.MetadataObject) (string, bool) {
	parts := strings.Split(rel, "/")
	if len(parts) < 2 {
		return "", false
	}
	folder := parts[0]
	if t := decompByDir[folder]; t != nil {
		return t.Name + ":" + parts[1], true // parts[1] is the parent component name
	}
	obj, ok := byDir[folder]
	if !ok {
		return "", false
	}
	member := memberFromFile(parts[len(parts)-1], obj.Suffix)
	if obj.InFolder && len(parts) > 2 {
		// Folder-based types (reports, dashboards, …) carry the folder in the member.
		member = strings.Join(parts[1:len(parts)-1], "/") + "/" + member
	}
	return obj.Name + ":" + member, true
}

// enumerateSpecs walks dir and collects the deduplicated set of metadata
// specifiers for every component beneath it, preserving first-seen order.
func enumerateSpecs(dir string, proj *project.Project, byDir map[string]mdapi.MetadataObject) ([]string, error) {
	seen := map[string]bool{}
	var specs []string
	err := filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return nil
		}
		rel, rerr := sourceRelOf(p, proj)
		if rerr != nil {
			return nil
		}
		if spec, ok := relToSpec(rel, byDir); ok && !seen[spec] {
			seen[spec] = true
			specs = append(specs, spec)
		}
		return nil
	})
	return specs, err
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
// directory it lives in, with any leading "main/default" stripped, so the first
// segment is the metadata directory name (e.g. "objects", "layouts"). It returns
// "" for the package root or its main/default tree (i.e. "the whole project").
func sourceRelOf(path string, proj *project.Project) (string, error) {
	abs, err := filepath.Abs(path)
	if err != nil {
		return "", err
	}
	for _, dir := range proj.AbsDirs() {
		rel, err := filepath.Rel(dir, abs)
		if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
			continue
		}
		rel = filepath.ToSlash(rel)
		switch {
		case rel == ".", rel == "main/default":
			return "", nil
		default:
			return strings.TrimPrefix(rel, "main/default/"), nil
		}
	}
	return "", fmt.Errorf("%s is not under a package directory of %s", path, proj.Root)
}
