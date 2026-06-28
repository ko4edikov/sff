// Package source resolves local Salesforce metadata (flat files and LWC/Aura
// bundles), fetches the org counterpart via the Tooling API, and normalizes it
// for comparison. It is the native equivalent of the sf-compare bash script.
package source

import (
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
)

// Kind distinguishes flat metadata from bundle metadata.
type Kind int

const (
	Flat Kind = iota
	LWC
	Aura
)

// Target is a resolved piece of local metadata to compare against an org.
type Target struct {
	Kind      Kind
	Name      string // developer name (file base name, or bundle dir name)
	LocalPath string // a file for Flat, a bundle directory for LWC/Aura
	Object    string // Tooling object for Flat (e.g. "ApexClass")
	Field     string // source field for Flat (e.g. "Body")
}

// flatTypes maps a flat file extension to its Tooling object and source field.
var flatTypes = map[string]struct{ object, field string }{
	"cls":       {"ApexClass", "Body"},
	"trigger":   {"ApexTrigger", "Body"},
	"page":      {"ApexPage", "Markup"},
	"component": {"ApexComponent", "Markup"},
}

// Resolve turns a path or a bare name into a Target. A path (file or bundle dir)
// is used directly; a bare name is searched for from the current directory:
// first as an lwc/aura bundle, then as a flat metadata file.
func Resolve(arg string) (*Target, error) {
	if arg == "" {
		return nil, fmt.Errorf("no metadata file or name given")
	}
	if _, err := os.Stat(arg); err == nil {
		return classify(arg)
	}

	// Bundle by name: any directory ".../lwc/<arg>" or ".../aura/<arg>".
	for _, kind := range []string{"lwc", "aura"} {
		if dir := findDir(".", filepath.Join(kind, arg)); dir != "" {
			return classify(dir)
		}
	}
	// Flat by name: "<arg>.<ext>".
	for ext := range flatTypes {
		if f := findFile(".", arg+"."+ext); f != "" {
			return classify(f)
		}
	}
	return nil, fmt.Errorf("not found in project: %s", arg)
}

// classify builds a Target from a concrete path.
func classify(path string) (*Target, error) {
	if kind, root, name, ok := bundleOf(path); ok {
		return &Target{Kind: kind, Name: name, LocalPath: root}, nil
	}

	info, err := os.Stat(path)
	if err != nil {
		return nil, err
	}
	if info.IsDir() {
		return nil, fmt.Errorf("%s is a directory but not an lwc/aura bundle", path)
	}

	ext := strings.TrimPrefix(filepath.Ext(path), ".")
	t, ok := flatTypes[ext]
	if !ok {
		return nil, fmt.Errorf("unsupported metadata type: .%s", ext)
	}
	name := strings.TrimSuffix(filepath.Base(path), "."+ext)
	return &Target{Kind: Flat, Name: name, LocalPath: path, Object: t.object, Field: t.field}, nil
}

// bundleOf returns the bundle kind, root dir, and name if path lies within an
// lwc/ or aura/ directory.
func bundleOf(path string) (kind Kind, root, name string, ok bool) {
	abs, err := filepath.Abs(path)
	if err != nil {
		abs = path
	}
	parts := strings.Split(filepath.ToSlash(abs), "/")
	for i, p := range parts {
		if (p == "lwc" || p == "aura") && i+1 < len(parts) {
			root = strings.Join(parts[:i+2], "/")
			name = parts[i+1]
			if p == "lwc" {
				return LWC, root, name, true
			}
			return Aura, root, name, true
		}
	}
	return 0, "", "", false
}

// findDir returns the first directory under base whose path ends with suffix.
func findDir(base, suffix string) string {
	suffix = filepath.ToSlash(suffix)
	var found string
	_ = filepath.WalkDir(base, func(path string, d fs.DirEntry, err error) error {
		if err != nil || !d.IsDir() {
			return nil
		}
		if strings.HasSuffix(filepath.ToSlash(path), "/"+suffix) || filepath.ToSlash(path) == suffix {
			found = path
			return filepath.SkipAll
		}
		return nil
	})
	return found
}

// findFile returns the first regular file under base with the given base name.
func findFile(base, name string) string {
	var found string
	_ = filepath.WalkDir(base, func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return nil
		}
		if filepath.Base(path) == name {
			found = path
			return filepath.SkipAll
		}
		return nil
	})
	return found
}
