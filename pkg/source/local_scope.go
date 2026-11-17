package source

import (
	"io/fs"
	"os"
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
	seen := make(map[string]bool)
	var specs []string
	walkLocal(dirs, catalog, func(spec, _ string) {
		if !seen[spec] {
			seen[spec] = true
			specs = append(specs, spec)
		}
	})
	return specs
}

// walkLocal calls fn with the Type:Name specifier and absolute path of every
// local file that classifies as a metadata component.
func walkLocal(dirs []string, catalog *mdapi.DescribeResult, fn func(spec, path string)) {
	byDir := catalogByDir(catalog)
	typeDirs := knownTypeDirs(catalog)
	for _, root := range dirs {
		filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
			if err != nil || d.IsDir() {
				return nil
			}
			rel := relFromTypeDir(root, p, typeDirs)
			if rel == "" {
				return nil
			}
			if spec, ok := classifyLocalFile(strings.Split(rel, "/"), byDir); ok && spec != "" {
				fn(spec, p)
			}
			return nil
		})
	}
}

// LocalIndex locates the local files of org components.
type LocalIndex struct {
	files map[string][]string // Type:Name -> absolute file paths
}

// BuildLocalIndex scans dirs once so components can be looked up by Find.
func BuildLocalIndex(dirs []string, catalog *mdapi.DescribeResult) *LocalIndex {
	idx := &LocalIndex{files: map[string][]string{}}
	walkLocal(dirs, catalog, func(spec, p string) { idx.files[spec] = append(idx.files[spec], p) })
	return idx
}

// Find returns the local path to diff or open for the org component typ:name:
// the exact file for a decomposed child (a CustomField, a ValidationRule, …), the
// component's directory for a decomposed parent or a bundle with several files,
// and the file itself otherwise. ok is false when the project doesn't track it.
func (l *LocalIndex) Find(typ, name string) (string, bool) {
	// Decomposed child ("CustomField", "Account.X__c") -> its own file.
	for _, parent := range decompByDir {
		for _, c := range parent.Children {
			if c.Type != typ {
				continue
			}
			pname, cname, found := strings.Cut(name, ".")
			if !found {
				continue
			}
			for _, f := range l.files[parent.Name+":"+pname] {
				if filepath.Base(f) == pname+"."+parent.Suffix+"-meta.xml" {
					child := filepath.Join(filepath.Dir(f), c.XMLTag, cname+"."+c.Suffix+"-meta.xml")
					if _, err := os.Stat(child); err == nil {
						return child, true
					}
				}
			}
		}
	}
	files := l.files[typ+":"+name]
	switch len(files) {
	case 0:
		return "", false
	case 1:
		return files[0], true
	}
	dir := filepath.Dir(files[0])
	for _, f := range files[1:] {
		for !strings.HasPrefix(f, dir+string(filepath.Separator)) {
			dir = filepath.Dir(dir)
		}
	}
	return dir, true
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

// ScopeWithChildren extends Type:Name specifiers (as LocalScope returns them)
// with the child components of every decomposed parent: "CustomObject:Account"
// adds "CustomField:Account.*", "ValidationRule:Account.*", and so on; a bare
// singleton such as "CustomLabels" adds "CustomLabel:*". The org tracks and lists
// those children as components of their own, so a scope made only of parents
// would never match a change to a single field.
func ScopeWithChildren(specs []string) []string {
	children := map[string][]string{} // parent type -> child types
	for _, t := range decompByDir {
		for _, c := range t.Children {
			children[t.Name] = append(children[t.Name], c.Type)
		}
	}
	out := append([]string(nil), specs...)
	seen := map[string]bool{}
	for _, s := range specs {
		seen[s] = true
	}
	for _, s := range specs {
		typ, name, hasName := strings.Cut(s, ":")
		member := "*"
		if hasName {
			member = name + ".*"
		}
		for _, child := range children[typ] {
			spec := child + ":" + member
			if !seen[spec] {
				seen[spec] = true
				out = append(out, spec)
			}
		}
	}
	return out
}
