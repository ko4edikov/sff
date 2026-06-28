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

	"github.com/ko4edikov/sff/internal/mdapi"
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

	byDir := catalogByDir(catalog)
	known := knownDirs(byDir)

	r := &recomposer{
		byDir:      byDir,
		entries:    map[string][]byte{},
		members:    map[string]map[string]bool{},
		decomposed: map[string]*decompGroup{},
		statics:    map[string]*staticGroup{},
	}

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
		metaRel := strings.Join(segs[idx:], "/")
		folder := segs[idx]

		data, derr := os.ReadFile(p)
		if derr != nil {
			return fmt.Errorf("read %s: %w", rel, derr)
		}
		return r.route(folder, metaRel, segs[idx:], data)
	})
	if err != nil {
		return nil, err
	}

	if err := r.flushDecomposed(); err != nil {
		return nil, err
	}
	if err := r.flushStatics(); err != nil {
		return nil, err
	}

	if len(r.entries) == 0 {
		return nil, fmt.Errorf("no deployable metadata found under %s", root)
	}
	return &RecomposeResult{Entries: r.entries, Package: r.buildPackage(version), Warnings: r.warnings}, nil
}

// recomposer accumulates state across the directory walk.
type recomposer struct {
	byDir      map[string]mdapi.MetadataObject
	entries    map[string][]byte          // metadata path → bytes
	members    map[string]map[string]bool // type → member set
	decomposed map[string]*decompGroup    // component dir → its files
	statics    map[string]*staticGroup    // resource name → its files
	warnings   []string
}

type decompGroup struct {
	t        *DecompType
	name     string
	parent   []byte
	children []decompChildFile
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
// metadata-format file.
func (r *recomposer) flushDecomposed() error {
	for _, g := range r.decomposed {
		data, err := recomposeDecomposed(g)
		if err != nil {
			return err
		}
		dest := path.Join(g.t.DirectoryName, g.name+"."+g.t.Suffix)
		r.entries[dest] = data
		r.addMember(g.t.Name, g.name)
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
	lines := splitLines(parent)

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
