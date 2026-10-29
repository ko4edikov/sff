package source

import (
	"encoding/xml"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/ko4edikov/sff/pkg/mdapi"
	"github.com/ko4edikov/sff/pkg/project"
)

// DeletePlan is what `sff delete` removes: the destructive manifest to send to
// the org and the matching local source files to remove once the org-side
// delete succeeds.
type DeletePlan struct {
	Package *mdapi.Package // components for destructiveChanges.xml (no wildcards)
	Files   []string       // absolute paths of local files to remove, sorted

	// LabelEdits maps a CustomLabels.labels-meta.xml path to the label names to
	// strip from it (labels have no file of their own).
	LabelEdits map[string][]string
	Warnings   []string

	typeDirs map[string]bool
}

// deleteSpec is one concrete (non-wildcard) component to delete.
type deleteSpec struct{ typ, member string }

// ResolveDelete works out which components to delete and which local files back
// them. Components come from paths (files or directories inside the project —
// each file maps to its narrowest component, so a field file is just that field
// while an object's residual file is the whole object) and/or from pkg (-m
// specifiers; a "*" member is expanded from the local files). A selection that
// includes a decomposed parent subsumes its children, and a bundle or flat
// component always brings along every file that makes it up (sidecar
// -meta.xml, the whole lwc/aura directory, an expanded static resource, …).
// Components with no local source are dropped with a warning.
func ResolveDelete(proj *project.Project, paths []string, pkg *mdapi.Package, version string, catalog *mdapi.DescribeResult) (*DeletePlan, error) {
	byDir := catalogByDir(catalog)
	byType := byTypeName(catalog)
	typeDirs := knownTypeDirs(catalog)
	roots := memberRoots(proj)
	plan := &DeletePlan{typeDirs: typeDirs}

	var specs []deleteSpec
	seen := map[deleteSpec]bool{}
	add := func(s deleteSpec) {
		if !seen[s] {
			seen[s] = true
			specs = append(specs, s)
		}
	}

	for _, p := range paths {
		abs, err := filepath.Abs(p)
		if err != nil {
			return nil, err
		}
		fi, err := os.Stat(abs)
		if err != nil {
			return nil, fmt.Errorf("%s: %w", p, err)
		}
		files := []string{abs}
		if fi.IsDir() {
			files = walkFiles(abs)
		}
		for _, f := range files {
			parts := projectRelParts(proj, f, typeDirs)
			spec, ok := classifyDeleteFile(parts, byDir)
			switch {
			case ok:
				add(spec)
			case !fi.IsDir():
				plan.Warnings = append(plan.Warnings, fmt.Sprintf("%s is not a deletable metadata component; skipped", p))
			}
		}
	}

	if pkg != nil {
		for _, t := range pkg.Types {
			for _, member := range t.Members {
				if member != "*" || t.Name == "CustomLabel" {
					add(deleteSpec{t.Name, member})
					continue
				}
				files, err := resolveSpecFiles(roots, t.Name, member, byType)
				if err != nil {
					return nil, err
				}
				for _, f := range files {
					if spec, ok := classifyDeleteFile(strings.Split(f.metaRel, "/"), byDir); ok {
						add(spec)
					}
				}
				if len(files) == 0 {
					plan.Warnings = append(plan.Warnings, fmt.Sprintf("%s:* not found in project", t.Name))
				}
			}
		}
	}

	fileSet := map[string]bool{}
	members := map[string][]string{}
	for _, s := range specs {
		if parentSelected(s, seen) {
			continue // the parent's delete takes the child with it
		}
		if s.typ == "CustomLabel" {
			// Labels share one CustomLabels file, so deleting one means editing
			// that file rather than removing a file of its own.
			names := []string{s.member}
			if s.member == "*" {
				names = allLabelNames(roots)
			}
			for _, name := range names {
				path, ok := findLabelsFile(roots, name)
				if !ok {
					plan.Warnings = append(plan.Warnings, fmt.Sprintf("CustomLabel:%s not found in project", name))
					continue
				}
				if plan.LabelEdits == nil {
					plan.LabelEdits = map[string][]string{}
				}
				plan.LabelEdits[path] = append(plan.LabelEdits[path], name)
				members[s.typ] = append(members[s.typ], name)
			}
			continue
		}
		files, err := resolveSpecFiles(roots, s.typ, s.member, byType)
		if err != nil {
			return nil, err
		}
		if len(files) == 0 {
			plan.Warnings = append(plan.Warnings, fmt.Sprintf("%s:%s not found in project", s.typ, s.member))
			continue
		}
		for _, f := range files {
			fileSet[f.path] = true
		}
		members[s.typ] = append(members[s.typ], s.member)
	}
	if len(members) == 0 {
		return nil, fmt.Errorf("no source-backed components to delete under %s", proj.Root)
	}

	plan.Package = &mdapi.Package{Version: strings.TrimPrefix(version, "v")}
	types := make([]string, 0, len(members))
	for t := range members {
		types = append(types, t)
	}
	sort.Strings(types)
	for _, t := range types {
		m := members[t]
		sort.Strings(m)
		plan.Package.Types = append(plan.Package.Types, mdapi.PackageTypes{Name: t, Members: m})
	}
	for f := range fileSet {
		plan.Files = append(plan.Files, f)
	}
	sort.Strings(plan.Files)
	return plan, nil
}

// RemoveLocal strips deleted labels from their CustomLabels file, deletes the plan's files and then any directories they leave
// empty, stopping at the metadata type directory (e.g. classes/) so the
// project skeleton stays in place.
func (p *DeletePlan) RemoveLocal() error {
	for path, names := range p.LabelEdits {
		if err := removeLabels(path, names); err != nil {
			return err
		}
	}
	for _, f := range p.Files {
		if err := os.Remove(f); err != nil && !os.IsNotExist(err) {
			return fmt.Errorf("remove %s: %w", f, err)
		}
	}
	for _, f := range p.Files {
		for dir := filepath.Dir(f); !p.typeDirs[filepath.Base(dir)]; dir = filepath.Dir(dir) {
			if os.Remove(dir) != nil { // non-empty (or gone): leave it
				break
			}
		}
	}
	return nil
}

// resolveSpecFiles finds the local files of one component, routing decomposed
// child types (CustomField, …) to their own resolver.
func resolveSpecFiles(roots []string, typ, member string, byType map[string]mdapi.MetadataObject) ([]memberFile, error) {
	if rule, ok := childTypeIndex[typ]; ok {
		return resolveChildMemberFiles(roots, rule, member)
	}
	return resolveMemberFiles(roots, typ, member, byType)
}

// parentSelected reports whether s is a decomposed child whose parent component
// is also being deleted.
func parentSelected(s deleteSpec, selected map[deleteSpec]bool) bool {
	rule, ok := childTypeIndex[s.typ]
	if !ok {
		return false
	}
	parent, _, _ := strings.Cut(s.member, ".")
	return selected[deleteSpec{rule.parent.Name, parent}]
}

// projectRelParts returns p's path split from its metadata type directory
// (["classes", "Foo.cls"]), or nil if p isn't under a package directory.
func projectRelParts(proj *project.Project, p string, typeDirs map[string]bool) []string {
	for _, root := range proj.AbsDirs() {
		if rel := relFromTypeDir(root, p, typeDirs); rel != "" {
			return strings.Split(rel, "/")
		}
	}
	return nil
}

// classifyDeleteFile maps a type-relative source path to the component that
// owns it: a decomposed child to its own type, a bundle or static-resource file
// to the bundle/resource, and a flat file to its member. ok is false for files
// that aren't deletable components (singletons like CustomLabels, strays).
func classifyDeleteFile(parts []string, byDir map[string]mdapi.MetadataObject) (deleteSpec, bool) {
	if len(parts) < 2 {
		return deleteSpec{}, false
	}
	folder := parts[0]

	if t := decompByDir[folder]; t != nil {
		if spec, ok := childSpec(strings.Join(parts, "/")); ok {
			typ, member, _ := strings.Cut(spec, ":")
			return deleteSpec{typ, member}, true
		}
		member := parts[1]
		if len(parts) == 2 {
			member = memberFromFile(parts[1], t.Suffix)
		}
		return deleteSpec{t.Name, member}, true
	}

	typ := dirNameToType(folder, byDir)
	if typ == "" {
		return deleteSpec{}, false
	}
	suffix, bundle := "", bundleDirs[folder]
	if o, ok := byDir[folder]; ok {
		suffix, bundle = o.Suffix, o.Suffix == ""
	}

	switch {
	case typ == "StaticResource":
		member := parts[1]
		if len(parts) == 2 {
			member = memberStem(parts[1])
		}
		return deleteSpec{typ, member}, true
	case bundle:
		return deleteSpec{typ, parts[1]}, len(parts) > 2
	}

	member := memberFromFile(parts[len(parts)-1], suffix)
	if len(parts) > 2 { // in-folder types carry the folder in the member
		member = strings.Join(parts[1:len(parts)-1], "/") + "/" + member
	}
	if member == typ {
		return deleteSpec{}, false // whole-file singleton (CustomLabels)
	}
	return deleteSpec{typ, member}, true
}

// labelsFiles lists the CustomLabels files under roots.
func labelsFiles(roots []string) []string {
	var out []string
	for _, root := range roots {
		if p := filepath.Join(root, "labels", "CustomLabels.labels-meta.xml"); isFile(p) {
			out = append(out, p)
		}
	}
	return out
}

// findLabelsFile returns the CustomLabels file that defines the named label.
func findLabelsFile(roots []string, name string) (string, bool) {
	for _, p := range labelsFiles(roots) {
		if data, err := os.ReadFile(p); err == nil {
			if _, ok := extractLabelBlock(data, name); ok {
				return p, true
			}
		}
	}
	return "", false
}

// allLabelNames lists every label name defined in the project's CustomLabels files.
func allLabelNames(roots []string) []string {
	var names []string
	for _, p := range labelsFiles(roots) {
		data, err := os.ReadFile(p)
		if err != nil {
			continue
		}
		var doc customLabelsDoc
		if xml.Unmarshal(data, &doc) != nil {
			continue
		}
		for _, l := range doc.Labels {
			names = append(names, l.FullName)
		}
	}
	return names
}

// removeLabels rewrites a CustomLabels file without the named <labels> blocks.
func removeLabels(path string, names []string) error {
	data, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	fi, err := os.Stat(path)
	if err != nil {
		return err
	}
	return os.WriteFile(path, dropLabelBlocks(data, names), fi.Mode().Perm())
}

// dropLabelBlocks removes each <labels>…</labels> block whose fullName is in
// names, line by line so the rest of the file (indentation, line endings) is
// preserved byte for byte.
func dropLabelBlocks(data []byte, names []string) []byte {
	drop := map[string]bool{}
	for _, n := range names {
		drop[n] = true
	}
	lines := strings.SplitAfter(string(data), "\n")
	var out strings.Builder
	for i := 0; i < len(lines); i++ {
		if strings.TrimSpace(lines[i]) != "<labels>" {
			out.WriteString(lines[i])
			continue
		}
		end := i
		for end < len(lines) && strings.TrimSpace(lines[end]) != "</labels>" {
			end++
		}
		if end >= len(lines) {
			out.WriteString(lines[i]) // unterminated: leave untouched
			continue
		}
		if drop[extractFullName(lines[i+1:end])] {
			i = end
			continue
		}
		for ; i <= end; i++ {
			out.WriteString(lines[i])
		}
		i = end
	}
	return []byte(out.String())
}
