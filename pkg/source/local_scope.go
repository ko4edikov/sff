package source

import (
	"io/fs"
	"path/filepath"
	"strings"

	"github.com/ko4edikov/sff/pkg/mdapi"
)

// LocalScope scans dirs (a project's package directories) and returns the
// Metadata API Type:Name specifiers already tracked locally — e.g.
// "ApexClass:Foo", "CustomObject:Account", or the bare type name
// "CustomLabels" for a whole-file singleton type (one file shared by every
// component of that type, named after the type itself). Specifiers are
// deduplicated and match the syntax mdapi.BuildPackage/ParseSpecifiers
// accept, so the result can be fed straight into a retrieve.
//
// Classification reuses the same tables convert.go and place_index.go use
// (decompByDir, dirToType, the describe catalog) and place_index.go's
// relFromTypeDir to locate each file's type-relative path, so a project's
// scope is always computed the same way its files are placed, regardless of
// how deep a custom module layout nests them.
func LocalScope(dirs []string, catalog *mdapi.DescribeResult) []string {
	byDir := catalogByDir(catalog)
	typeDirs := knownTypeDirs(catalog)
	seen := make(map[string]bool)
	var specs []string
	add := func(spec string) {
		if spec != "" && !seen[spec] {
			seen[spec] = true
			specs = append(specs, spec)
		}
	}

	for _, root := range dirs {
		filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
			if err != nil || d.IsDir() {
				return nil
			}
			rel := relFromTypeDir(root, p, typeDirs)
			if rel == "" {
				return nil
			}
			parts := strings.Split(rel, "/")
			if spec, ok := classifyLocalFile(parts, byDir); ok {
				add(spec)
			}
			return nil
		})
	}
	return specs
}

// classifyLocalFile turns a type-relative path (parts[0] is the type
// directory, e.g. "classes"; parts is "/"-joined, already relative to the
// type directory as produced by relFromTypeDir) into a Type:Name specifier.
func classifyLocalFile(parts []string, byDir map[string]mdapi.MetadataObject) (string, bool) {
	typeDir := parts[0]

	// Decomposed types (objects, …): parts[1] is the component's own name
	// (e.g. "Account"), shared by every one of its child files regardless of
	// which child file we're looking at.
	if t := decompByDir[typeDir]; t != nil {
		if len(parts) < 2 {
			return "", false
		}
		return t.Name + ":" + parts[1], true
	}

	typeName := dirNameToType(typeDir, byDir)
	if typeName == "" {
		return "", false
	}
	if len(parts) < 2 {
		return "", false
	}
	if isVerbatim(typeDir, byDir) && (bundleDirs[typeDir] || (byDir != nil && byDir[typeDir].Suffix == "")) {
		return typeName + ":" + parts[1], true // bundle: dir/Name/...
	}
	if len(parts) != 2 {
		return "", false // a bundle member file under an unrecognized bundle dir
	}
	stem := memberStem(parts[1])
	if stem == typeName {
		// A whole-file singleton type: the file is named after the type
		// itself (e.g. "labels/CustomLabels.labels-meta.xml"), so there's no
		// per-component name to key on — the bare type name is the specifier.
		return typeName, true
	}
	return typeName + ":" + stem, true
}

// dirNameToType resolves a source directory name back to its Metadata API
// type name, preferring the describe catalog and falling back to the static
// no-catalog map (dirToType, defined in recompose.go).
func dirNameToType(dir string, byDir map[string]mdapi.MetadataObject) string {
	if byDir != nil {
		if o, ok := byDir[dir]; ok {
			return o.Name
		}
	}
	return dirToType[dir]
}

// memberStem strips a trailing "-meta.xml" and the file's extension, leaving
// the component's Metadata API name (e.g. "Foo.cls-meta.xml" → "Foo").
func memberStem(name string) string {
	name = strings.TrimSuffix(name, "-meta.xml")
	if i := strings.LastIndex(name, "."); i >= 0 {
		name = name[:i]
	}
	return name
}
