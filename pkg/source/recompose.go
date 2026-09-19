package source

import (
	"archive/zip"
	"bytes"
	"fmt"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"

	"github.com/ko4edikov/sff/pkg/mdapi"
	"github.com/ko4edikov/sff/pkg/project"
)

// RecomposeResult is the outcome of a source→metadata recompose: the
// metadata-format files (forward-slash paths, excluding package.xml) and the
// manifest describing them.
type RecomposeResult struct {
	Entries  map[string][]byte
	Package  *mdapi.Package
	Warnings []string
}

// dirToType is the fallback directory→Metadata API type map, consulted only when
// no describe catalog is available. With a catalog, types come from its
// directoryName→xmlName mapping.
var dirToType = map[string]string{
	"classes":         "ApexClass",
	"triggers":        "ApexTrigger",
	"pages":           "ApexPage",
	"components":      "ApexComponent",
	"lwc":             "LightningComponentBundle",
	"aura":            "AuraDefinitionBundle",
	"staticresources": "StaticResource",
	"objects":         "CustomObject",
	"layouts":         "Layout",
	"permissionsets":  "PermissionSet",
	"profiles":        "Profile",
	"flows":           "Flow",
	"labels":          "CustomLabels",
	"tabs":            "CustomTab",
	"applications":    "CustomApplication",
}

// bundleDirs is the fallback set of folder-per-component bundle directories,
// consulted only when no catalog is available (a catalog flags bundles by an
// empty suffix).
var bundleDirs = map[string]bool{"lwc": true, "aura": true}

// ignoredInBundle lists files sf's default forceignore excludes from a deploy;
// keeping them out avoids "unknown file" errors when packing LWC/Aura bundles.
func ignoredInBundle(rel string) bool {
	base := path.Base(rel)
	return base == "jsconfig.json" ||
		base == ".eslintrc.json" ||
		strings.HasSuffix(base, ".test.js") ||
		strings.Contains("/"+rel+"/", "/__tests__/")
}

// RecomposeDir walks a source-format directory tree under root and produces
// metadata-format zip entries plus a package.xml manifest at the given API
// version. catalog, when non-nil, drives type and verbatim classification;
// otherwise the built-in fallbacks are used.
func RecomposeDir(root, version string, catalog *mdapi.DescribeResult) (*RecomposeResult, error) {
	info, err := os.Stat(root)
	if err != nil {
		return nil, err
	}
	if !info.IsDir() {
		return nil, fmt.Errorf("%s is not a directory", root)
	}

	r := newRecomposer(catalog)
	known := knownDirs(r.byDir)

	err = filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		rel, rerr := filepath.Rel(root, p)
		if rerr != nil {
			return rerr
		}
		segs := strings.Split(filepath.ToSlash(rel), "/")
		idx := firstKnownIndex(segs, known)
		if idx < 0 {
			return nil // outside any recognized metadata folder; ignore
		}
		data, derr := os.ReadFile(p)
		if derr != nil {
			return fmt.Errorf("read %s: %w", rel, derr)
		}
		r.ingest(strings.Join(segs[idx:], "/"), data)
		return nil
	})
	if err != nil {
		return nil, err
	}
	if err := r.flush(); err != nil {
		return nil, err
	}
	if len(r.entries) == 0 {
		return nil, fmt.Errorf("no deployable metadata found under %s", root)
	}
	return &RecomposeResult{Entries: r.entries, Package: r.buildPackage(version), Warnings: r.warnings}, nil
}

// RecomposeMembers resolves the components named in pkg to their source files
// under proj, recomposes them, and returns the metadata-format entries plus a
// manifest of what was actually found. Members with no local files are reported
// as warnings (not errors), mirroring how a manifest may over-list.
func RecomposeMembers(proj *project.Project, pkg *mdapi.Package, version string, catalog *mdapi.DescribeResult) (*RecomposeResult, error) {
	r := newRecomposer(catalog)
	byType := byTypeName(catalog)
	roots := memberRoots(proj)

	for _, t := range pkg.Types {
		for _, member := range t.Members {
			// "CustomLabel" (singular) is the Metadata API's child type for one
			// individual label; unlike other flat types it has no file of its
			// own — all labels share one CustomLabels.labels-meta.xml — so it
			// needs its own resolution path instead of resolveMemberFiles.
			if t.Name == "CustomLabel" && member != "*" {
				block, ok := resolveCustomLabelMember(roots, member)
				if !ok {
					r.warnings = append(r.warnings, fmt.Sprintf("%s:%s not found in project", t.Name, member))
					continue
				}
				r.addLabel(member, block)
				continue
			}

			// A decomposed child type (CustomField, ValidationRule, …) selected
			// directly still deploys with the whole composed parent file in the
			// zip — the Metadata API rejects a lone "objects/Account/fields/
			// X__c.field" entry ("was not found in zipped directory") — but the
			// manifest member stays narrow (e.g. "CustomField:Account.X__c"),
			// matching how `sf project deploy start -m CustomField:...` behaves.
			if rule, ok := childTypeIndex[t.Name]; ok {
				if err := r.selectDecomposedChild(roots, rule, t.Name, member, byType); err != nil {
					return nil, err
				}
				continue
			}

			typeName := t.Name
			if typeName == "CustomLabel" { // "*": every label, i.e. the whole shared file
				typeName = "CustomLabels"
			}
			if t := decompByName[typeName]; t != nil && member != "*" {
				// A whole decomposed parent (re-)selected by its own type name
				// wins over any narrower child selections queued for it, and
				// skips re-resolving/re-ingesting a component already pulled in
				// (e.g. by an earlier narrow child specifier for the same parent).
				if g, exists := r.decomposed[t.DirectoryName+"/"+member]; exists {
					g.fullSelected = true
					continue
				}
			}
			files, err := resolveMemberFiles(roots, typeName, member, byType)
			if err != nil {
				return nil, err
			}
			if len(files) == 0 {
				r.warnings = append(r.warnings, fmt.Sprintf("%s:%s not found in project", t.Name, member))
				continue
			}
			for _, f := range files {
				r.ingest(f.metaRel, f.data)
				r.markFullySelected(f.metaRel)
			}
		}
	}

	if err := r.flush(); err != nil {
		return nil, err
	}
	if len(r.entries) == 0 {
		return nil, fmt.Errorf("none of the requested metadata was found under %s", proj.Root)
	}
	return &RecomposeResult{Entries: r.entries, Package: r.buildPackage(version), Warnings: r.warnings}, nil
}

// newRecomposer builds an empty recomposer keyed off the describe catalog.
func newRecomposer(catalog *mdapi.DescribeResult) *recomposer {
	return &recomposer{
		byDir:      catalogByDir(catalog),
		entries:    map[string][]byte{},
		members:    map[string]map[string]bool{},
		decomposed: map[string]*decompGroup{},
		statics:    map[string]*staticGroup{},
		labels:     map[string][]byte{},
	}
}

// ingest routes one source file, identified by its metadata-relative path (which
// starts at the type directory, e.g. "classes/Foo.cls").
func (r *recomposer) ingest(metaRel string, data []byte) {
	segs := strings.Split(metaRel, "/")
	r.route(segs[0], metaRel, segs, data)
}

// flush composes all buffered decomposed components, individually selected
// custom labels, and static resources.
func (r *recomposer) flush() error {
	if err := r.flushDecomposed(); err != nil {
		return err
	}
	r.flushLabels()
	return r.flushStatics()
}

// recomposer accumulates state across the directory walk.
type recomposer struct {
	byDir      map[string]mdapi.MetadataObject
	entries    map[string][]byte          // metadata path → bytes
	members    map[string]map[string]bool // type → member set
	decomposed map[string]*decompGroup    // component dir → its files
	statics    map[string]*staticGroup    // resource name → its files
	labels     map[string][]byte          // individually selected label fullName → its <labels> block
	warnings   []string
}

type decompGroup struct {
	t        *DecompType
	name     string
	parent   []byte
	children []decompChildFile
	// fullSelected marks a component pulled in via its own parent type (or a
	// -d directory walk) — its whole self is the manifest member. Left false
	// when the component was only pulled in to back a narrower child
	// selection (e.g. "CustomField:Account.X__c"), in which case narrow lists
	// the specific child members to register instead.
	fullSelected bool
	narrow       []narrowMember
}

// narrowMember is one decomposed-child manifest member (e.g.
// "CustomField":"Account.X__c") queued for a component that was selected only
// through that child, not through its own parent type.
type narrowMember struct {
	typ    string
	member string
}

type decompChildFile struct {
	child DecompChild
	data  []byte
}

type staticGroup struct {
	name    string
	meta    []byte
	content []staticContentFile // path relative to the resource, plus bytes
}

type staticContentFile struct {
	rel  string
	data []byte
}

// route dispatches one source file to the right recomposition path.
func (r *recomposer) route(folder, metaRel string, segs []string, data []byte) error {
	switch {
	case folder == "staticresources":
		r.routeStatic(segs, data)
	case decompByDir[folder] != nil:
		r.routeDecomposed(folder, segs, data)
	case r.isBundle(folder):
		r.routeBundle(folder, metaRel, segs, data)
	default:
		r.routeFlat(folder, metaRel, data)
	}
	return nil
}

// routeFlat handles a single-file (non-decomposed, non-bundle) component.
func (r *recomposer) routeFlat(folder, metaRel string, data []byte) {
	typ := r.typeFor(folder)
	if typ == "" {
		r.warnings = append(r.warnings, "skipped "+metaRel+" (unknown metadata type)")
		return
	}
	dest := metaRel
	if !isVerbatim(folder, r.byDir) {
		// XML-only types lose the source-only "-meta.xml" suffix in metadata format.
		dest = strings.TrimSuffix(metaRel, "-meta.xml")
	}
	r.entries[dest] = data
	r.addMember(typ, memberName(folder, metaRel))
}

// routeBundle copies an LWC/Aura bundle file verbatim and records the bundle as
// one member.
func (r *recomposer) routeBundle(folder, metaRel string, segs []string, data []byte) {
	if ignoredInBundle(metaRel) {
		return
	}
	r.entries[metaRel] = data
	if len(segs) >= 2 {
		r.addMember(r.typeFor(folder), segs[1])
	}
}

// routeDecomposed buckets a file of a decomposed type by its component directory.
func (r *recomposer) routeDecomposed(folder string, segs []string, data []byte) {
	t := decompByDir[folder]
	if len(segs) < 2 {
		return
	}
	name := segs[1]
	key := folder + "/" + name
	g := r.decomposed[key]
	if g == nil {
		g = &decompGroup{t: t, name: name}
		r.decomposed[key] = g
	}

	base := path.Base(strings.Join(segs, "/"))
	if base == name+"."+t.Suffix+"-meta.xml" {
		g.parent = data
		return
	}
	if child, ok := childForFile(t, base); ok {
		g.children = append(g.children, decompChildFile{child: child, data: data})
		return
	}
	r.warnings = append(r.warnings, "skipped "+strings.Join(segs, "/")+" (no matching child rule for "+t.Name+")")
}

// routeStatic buckets a static-resource file (its .resource-meta.xml or content)
// by resource name.
func (r *recomposer) routeStatic(segs []string, data []byte) {
	if len(segs) < 2 {
		return
	}
	rest := strings.Join(segs[1:], "/")
	var name, contentRel string
	switch {
	case strings.Contains(rest, "/"): // archive expanded into a directory
		name = segs[1]
		contentRel = strings.Join(segs[2:], "/")
	case strings.HasSuffix(rest, ".resource-meta.xml"):
		name = strings.TrimSuffix(rest, ".resource-meta.xml")
	default: // single content file, named after the resource with a content ext
		name = strings.TrimSuffix(rest, path.Ext(rest))
		contentRel = rest
	}

	g := r.statics[name]
	if g == nil {
		g = &staticGroup{name: name}
		r.statics[name] = g
	}
	if contentRel == "" && strings.HasSuffix(rest, ".resource-meta.xml") {
		g.meta = data
		return
	}
	g.content = append(g.content, staticContentFile{rel: contentRel, data: data})
}

// flushDecomposed composes each buffered decomposed component into one
// metadata-format file. The manifest member is the whole component, unless it
// was pulled in only to back one or more narrower child selections (queued in
// narrow), in which case those child members are registered instead.
func (r *recomposer) flushDecomposed() error {
	for _, g := range r.decomposed {
		data, err := recomposeDecomposed(g)
		if err != nil {
			return err
		}
		dest := path.Join(g.t.DirectoryName, g.name+"."+g.t.Suffix)
		r.entries[dest] = data
		if len(g.narrow) > 0 && !g.fullSelected {
			for _, nm := range g.narrow {
				r.addMember(nm.typ, nm.member)
			}
			continue
		}
		r.addMember(g.t.Name, g.name)
	}
	return nil
}

// markFullySelected records that the component owning metaRel was pulled in
// via its own parent type (or a directory walk), so flushDecomposed registers
// the whole component rather than any narrower child selections queued for it.
func (r *recomposer) markFullySelected(metaRel string) {
	segs := strings.Split(metaRel, "/")
	if len(segs) < 2 {
		return
	}
	if g := r.decomposed[segs[0]+"/"+segs[1]]; g != nil {
		g.fullSelected = true
	}
}

// selectDecomposedChild resolves one decomposed-child specifier (e.g.
// "CustomField:Account.X__c" or the wildcard "CustomField:*") by pulling the
// whole parent component into the zip — the Metadata API needs the composed
// parent file even for a single field — while queuing the requested child
// member(s) narrowly, so the manifest lists just the child(ren) asked for
// instead of the whole parent (unless the parent itself is also separately
// selected; see the fullSelected/narrow handling in flushDecomposed).
func (r *recomposer) selectDecomposedChild(roots []string, rule childRule, typeName, member string, byType map[string]mdapi.MetadataObject) error {
	if member == "*" {
		matches, err := resolveChildMemberFiles(roots, rule, "*")
		if err != nil {
			return err
		}
		if len(matches) == 0 {
			r.warnings = append(r.warnings, fmt.Sprintf("%s:* not found in project", typeName))
			return nil
		}
		seen := map[string]bool{}
		for _, f := range matches {
			m := childMemberName(f.metaRel, rule)
			if seen[m] {
				continue
			}
			seen[m] = true
			if err := r.selectDecomposedChild(roots, rule, typeName, m, byType); err != nil {
				return err
			}
		}
		return nil
	}

	match, err := resolveChildMemberFiles(roots, rule, member)
	if err != nil {
		return err
	}
	if len(match) == 0 {
		r.warnings = append(r.warnings, fmt.Sprintf("%s:%s not found in project", typeName, member))
		return nil
	}

	parentName, _, _ := strings.Cut(member, ".")
	key := rule.parent.DirectoryName + "/" + parentName
	if _, exists := r.decomposed[key]; !exists {
		files, err := resolveMemberFiles(roots, rule.parent.Name, parentName, byType)
		if err != nil {
			return err
		}
		for _, f := range files {
			r.ingest(f.metaRel, f.data)
		}
	}
	if g := r.decomposed[key]; g != nil && !g.fullSelected {
		g.narrow = append(g.narrow, narrowMember{typ: typeName, member: member})
	}
	return nil
}

// flushStatics re-packs each buffered static resource (re-archiving directory
// resources) and emits the .resource plus its verbatim -meta.xml.
func (r *recomposer) flushStatics() error {
	for _, g := range r.statics {
		if g.meta == nil {
			r.warnings = append(r.warnings, "skipped static resource "+g.name+" (no .resource-meta.xml)")
			continue
		}
		ct := staticContentType(g.meta)
		binary, err := packStaticResource(g, ct)
		if err != nil {
			return fmt.Errorf("static resource %s: %w", g.name, err)
		}
		r.entries["staticresources/"+g.name+".resource"] = binary
		r.entries["staticresources/"+g.name+".resource-meta.xml"] = g.meta
		r.addMember("StaticResource", g.name)
	}
	return nil
}

// addLabel buffers one individually selected custom label (-m CustomLabel:Name),
// recording it as a "CustomLabel" member so the manifest lists the specific
// label rather than the whole CustomLabels type.
func (r *recomposer) addLabel(name string, block []byte) {
	r.labels[name] = block
	r.addMember("CustomLabel", name)
}

// flushLabels composes the buffered individually selected labels into one
// CustomLabels.labels-meta.xml entry. Skipped when a whole-file ingest (e.g. a
// wildcard "CustomLabel:*") already produced that entry, since it already
// covers every buffered label.
func (r *recomposer) flushLabels() {
	if len(r.labels) == 0 {
		return
	}
	const dest = "labels/CustomLabels.labels" // XML-only type: metadata format drops "-meta.xml"
	if _, exists := r.entries[dest]; exists {
		return
	}
	names := make([]string, 0, len(r.labels))
	for n := range r.labels {
		names = append(names, n)
	}
	sort.Strings(names)

	var b strings.Builder
	b.WriteString(`<?xml version="1.0" encoding="UTF-8"?>` + "\n")
	b.WriteString(`<CustomLabels xmlns="` + mdNamespace + `">` + "\n")
	for _, n := range names {
		b.Write(r.labels[n])
		b.WriteByte('\n')
	}
	b.WriteString("</CustomLabels>\n")
	r.entries[dest] = []byte(b.String())
}

// packStaticResource turns the buffered content back into the .resource binary:
// the single file's bytes, or a zip of the directory tree for archive types.
func packStaticResource(g *staticGroup, contentType string) ([]byte, error) {
	hasTree := false
	for _, c := range g.content {
		if strings.Contains(c.rel, "/") {
			hasTree = true
			break
		}
	}
	if !archiveContentTypes[contentType] && !hasTree {
		if len(g.content) != 1 {
			return nil, fmt.Errorf("expected a single content file, found %d", len(g.content))
		}
		return g.content[0].data, nil
	}

	files := append([]staticContentFile(nil), g.content...)
	sort.Slice(files, func(i, j int) bool { return files[i].rel < files[j].rel })
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	for _, c := range files {
		w, err := zw.Create(c.rel)
		if err != nil {
			return nil, err
		}
		if _, err := w.Write(c.data); err != nil {
			return nil, err
		}
	}
	if err := zw.Close(); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

// recomposeDecomposed reverses decompose: it folds the child files back into the
// residual parent as indented child elements, restoring the composed document.
func recomposeDecomposed(g *decompGroup) ([]byte, error) {
	parent := g.parent
	if parent == nil {
		parent = []byte(`<?xml version="1.0" encoding="UTF-8"?>` + "\n" +
			`<` + g.t.Name + ` xmlns="` + mdNamespace + `">` + "\n" +
			`</` + g.t.Name + `>` + "\n")
	}
	lines := expandInlineRoot(splitLines(parent), g.t.Name)

	closeTag := "</" + g.t.Name + ">"
	closeIdx := -1
	for i := len(lines) - 1; i >= 0; i-- {
		if strings.TrimSpace(lines[i]) == closeTag {
			closeIdx = i
			break
		}
	}
	if closeIdx < 0 {
		return nil, fmt.Errorf("%s: no closing <%s> in parent file", g.name, g.t.Name)
	}

	children := orderedChildren(g)
	var childLines []string
	for _, cf := range children {
		childLines = append(childLines, childElementLines(cf.data, cf.child)...)
	}

	out := make([]string, 0, len(lines)+len(childLines))
	out = append(out, lines[:closeIdx]...)
	out = append(out, childLines...)
	out = append(out, lines[closeIdx:]...)
	return []byte(strings.Join(out, "\n") + "\n"), nil
}

// expandInlineRoot splits a root element written on a single line — an empty
// "<CustomObject xmlns="...">…</CustomObject>" or a self-closing
// "<CustomObject .../>", as a hand-written or minimal source file (e.g. a
// bare Custom Metadata Type definition) may have — into separate opening and
// closing lines, so the line-based closing-tag search above (which expects
// retrieve's usual pretty-printed layout, one element per line) can find it.
func expandInlineRoot(lines []string, name string) []string {
	openTag := "<" + name
	closeTag := "</" + name + ">"
	out := make([]string, 0, len(lines)+1)
	for _, l := range lines {
		trimmed := strings.TrimSpace(l)
		switch {
		case hasTagPrefix(trimmed, openTag) && strings.HasSuffix(trimmed, closeTag):
			out = append(out, strings.TrimSuffix(trimmed, closeTag), closeTag)
		case hasTagPrefix(trimmed, openTag) && strings.HasSuffix(trimmed, "/>"):
			out = append(out, strings.TrimSuffix(trimmed, "/>")+">", closeTag)
		default:
			out = append(out, l)
		}
	}
	return out
}

// hasTagPrefix reports whether trimmed opens with tag as its own element name
// (not merely sharing a string prefix, e.g. "<CustomObject" must not match
// "<CustomObjectTranslation").
func hasTagPrefix(trimmed, tag string) bool {
	if !strings.HasPrefix(trimmed, tag) {
		return false
	}
	if len(trimmed) == len(tag) {
		return true
	}
	switch trimmed[len(tag)] {
	case '>', ' ', '\t', '/':
		return true
	}
	return false
}

// orderedChildren sorts a component's children by their type's declared order,
// then by fullName, so recomposition is deterministic.
func orderedChildren(g *decompGroup) []decompChildFile {
	order := map[string]int{}
	for i, c := range g.t.Children {
		order[c.XMLTag] = i
	}
	cs := append([]decompChildFile(nil), g.children...)
	sort.SliceStable(cs, func(i, j int) bool {
		oi, oj := order[cs[i].child.XMLTag], order[cs[j].child.XMLTag]
		if oi != oj {
			return oi < oj
		}
		return extractFullName(splitLines(cs[i].data)) < extractFullName(splitLines(cs[j].data))
	})
	return cs
}

// childElementLines renders one child file as the indented element block it
// occupied inside the composed parent: the standalone root tag becomes the
// parent's child tag, its xmlns is dropped, and every line is indented one level.
func childElementLines(data []byte, child DecompChild) []string {
	lines := splitLines(data)
	i := 0
	if i < len(lines) && strings.HasPrefix(strings.TrimSpace(lines[i]), "<?xml") {
		i++
	}
	for i < len(lines) && strings.TrimSpace(lines[i]) == "" {
		i++
	}
	body := append([]string(nil), lines[i:]...)
	if len(body) == 0 {
		return nil
	}

	open := strings.TrimSpace(body[0])
	if strings.HasSuffix(open, "/>") {
		body[0] = "<" + child.XMLTag + "/>"
	} else {
		body[0] = "<" + child.XMLTag + ">"
		body[len(body)-1] = "</" + child.XMLTag + ">"
	}
	for j := range body {
		if strings.TrimSpace(body[j]) != "" {
			body[j] = "    " + body[j]
		}
	}
	return body
}

// childForFile finds the child rule whose file suffix matches a decomposed file.
func childForFile(t *DecompType, base string) (DecompChild, bool) {
	for _, c := range t.Children {
		if strings.HasSuffix(base, "."+c.Suffix+"-meta.xml") {
			return c, true
		}
	}
	return DecompChild{}, false
}

// isBundle reports whether folder is a folder-per-component bundle directory.
func (r *recomposer) isBundle(folder string) bool {
	if r.byDir != nil {
		if o, ok := r.byDir[folder]; ok {
			return o.Suffix == ""
		}
	}
	return bundleDirs[folder]
}

// typeFor returns the Metadata API type name for a source directory.
func (r *recomposer) typeFor(folder string) string {
	if r.byDir != nil {
		if o, ok := r.byDir[folder]; ok && o.Name != "" {
			return o.Name
		}
	}
	return dirToType[folder]
}

// addMember records member under type, creating the type's set on first use.
func (r *recomposer) addMember(typ, member string) {
	if typ == "" || member == "" {
		return
	}
	set := r.members[typ]
	if set == nil {
		set = map[string]bool{}
		r.members[typ] = set
	}
	set[member] = true
}

// buildPackage renders the accumulated members as a manifest.
func (r *recomposer) buildPackage(version string) *mdapi.Package {
	pkg := &mdapi.Package{Version: version}
	types := make([]string, 0, len(r.members))
	for t := range r.members {
		types = append(types, t)
	}
	sort.Strings(types)
	for _, t := range types {
		members := make([]string, 0, len(r.members[t]))
		for m := range r.members[t] {
			members = append(members, m)
		}
		sort.Strings(members)
		pkg.Types = append(pkg.Types, mdapi.PackageTypes{Members: members, Name: t})
	}
	return pkg
}

// memberName derives a manifest member from a flat file's metadata-relative path:
// the path under the type folder with its "-meta.xml" and type suffix removed.
// In-folder types keep their folder prefix (e.g. "MyFolder/MyReport").
func memberName(folder, metaRel string) string {
	rel := strings.TrimPrefix(metaRel, folder+"/")
	rel = strings.TrimSuffix(rel, "-meta.xml")
	rel = strings.TrimSuffix(rel, path.Ext(rel))
	return rel
}

// knownDirs is the set of metadata directory names: every catalog directoryName
// plus the built-in decomposed/static/bundle fallbacks.
func knownDirs(byDir map[string]mdapi.MetadataObject) map[string]bool {
	known := map[string]bool{"staticresources": true}
	for d := range byDir {
		known[d] = true
	}
	for d := range decompByDir {
		known[d] = true
	}
	for d := range dirToType {
		known[d] = true
	}
	return known
}

// firstKnownIndex returns the index of the first path segment that names a known
// metadata directory, or -1 if none does.
func firstKnownIndex(segs []string, known map[string]bool) int {
	for i, s := range segs {
		if known[s] {
			return i
		}
	}
	return -1
}
