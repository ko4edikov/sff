package mdapi

import (
	"encoding/xml"
	"fmt"
	"os"
	"sort"
	"strings"
)

// metadataNS is the namespace used by both package.xml and the SOAP Metadata API.
const metadataNS = "http://soap.sforce.com/2006/04/metadata"

// Package is a metadata manifest, marshaling to/from package.xml.
type Package struct {
	XMLName xml.Name       `xml:"Package"`
	Xmlns   string         `xml:"xmlns,attr"`
	Types   []PackageTypes `xml:"types"`
	Version string         `xml:"version"`
}

// PackageTypes is one <types> entry: a metadata type and its members.
type PackageTypes struct {
	Members []string `xml:"members"`
	Name    string   `xml:"name"`
}

// typeAliases maps a few friendly names to their real Metadata API type names.
var typeAliases = map[string]string{
	"lwc":     "LightningComponentBundle",
	"aura":    "AuraDefinitionBundle",
	"apex":    "ApexClass",
	"class":   "ApexClass",
	"label":   "CustomLabel",
	"object":  "CustomObject",
	"page":    "ApexPage",
	"trigger": "ApexTrigger",
}

// TypeResolver canonicalizes user-supplied metadata type names case-insensitively.
// It combines the built-in friendly aliases with the org's describe catalog when
// one is supplied, so "permissionset", "PERMISSIONSET" and "PermissionSet" all
// resolve to the canonical API name. A nil resolver falls back to aliases only.
type TypeResolver struct {
	canon map[string]string // lower(name) -> canonical
}

// defaultResolver knows only the friendly aliases; used when a caller has no
// describe catalog to offer (e.g. offline recompose).
var defaultResolver = NewTypeResolver(nil)

// NewTypeResolver builds a resolver from the friendly aliases, any extra
// canonical type names, and — when d is non-nil — every type and child type in
// the describe catalog. extra lets a caller register a known, offline set of
// canonical names (e.g. the Tooling-deploy-supported types) so case-insensitive
// resolution works without a live describe call.
func NewTypeResolver(d *DescribeResult, extra ...string) *TypeResolver {
	canon := make(map[string]string, len(typeAliases)+len(extra))
	for k, v := range typeAliases {
		canon[k] = v
	}
	for _, name := range extra {
		canon[strings.ToLower(name)] = name
	}
	if d != nil {
		for _, o := range d.Objects {
			canon[strings.ToLower(o.Name)] = o.Name
			for _, child := range o.ChildXMLNames {
				canon[strings.ToLower(child)] = child
			}
		}
	}
	return &TypeResolver{canon: canon}
}

// Resolve returns the canonical type name for t, or t unchanged if unknown.
func (r *TypeResolver) Resolve(t string) string {
	if r == nil {
		r = defaultResolver
	}
	if canon, ok := r.canon[strings.ToLower(t)]; ok {
		return canon
	}
	return t
}

// ParseSpecifiers builds a Package from "-m" values. Each spec is either
// "Type:Name" (a specific member), "Type:a,b,c" (several members of one type) or
// a bare "Type" (wildcard "*"). Type names are canonicalized through r (nil uses
// aliases only). Members are grouped by type and the type order is preserved by
// first appearance.
func ParseSpecifiers(specs []string, version string, r *TypeResolver) (*Package, error) {
	if len(specs) == 0 {
		return nil, fmt.Errorf("no metadata specified")
	}
	byType := map[string][]string{}
	var order []string
	for _, spec := range specs {
		spec = strings.TrimSpace(spec)
		if spec == "" {
			continue
		}
		typ, memberList := spec, "*"
		if i := strings.Index(spec, ":"); i >= 0 {
			typ, memberList = spec[:i], spec[i+1:]
		}
		typ = r.Resolve(strings.TrimSpace(typ))
		if typ == "" {
			return nil, fmt.Errorf("invalid metadata specifier %q (want Type or Type:Name)", spec)
		}
		if _, seen := byType[typ]; !seen {
			order = append(order, typ)
		}
		for _, member := range strings.Split(memberList, ",") {
			member = strings.TrimSpace(member)
			if member == "" {
				return nil, fmt.Errorf("invalid metadata specifier %q (want Type or Type:Name)", spec)
			}
			byType[typ] = append(byType[typ], member)
		}
	}

	pkg := &Package{Xmlns: metadataNS, Version: numericVersion(version)}
	for _, typ := range order {
		members := byType[typ]
		sort.Strings(members)
		pkg.Types = append(pkg.Types, PackageTypes{Members: members, Name: typ})
	}
	return pkg, nil
}

// BuildPackage produces a manifest from the two mutually exclusive selection
// styles shared by retrieve and deploy: an existing package.xml (manifest, the
// "-x" flag) takes precedence, otherwise "-m" specifiers are parsed with type
// names canonicalized through r. It centralizes the manifest-or-specifiers
// branch so callers don't each repeat it.
func BuildPackage(manifest string, specs []string, version string, r *TypeResolver) (*Package, error) {
	if manifest != "" {
		return LoadManifest(manifest)
	}
	return ParseSpecifiers(specs, version, r)
}

// LoadManifest reads an existing package.xml for the "-x" flag.
func LoadManifest(path string) (*Package, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", path, err)
	}
	var pkg Package
	if err := xml.Unmarshal(data, &pkg); err != nil {
		return nil, fmt.Errorf("parse %s: %w", path, err)
	}
	pkg.Xmlns = metadataNS
	return &pkg, nil
}

// XML renders the package as a package.xml document.
func (p *Package) XML() ([]byte, error) {
	p.Xmlns = metadataNS
	out, err := xml.MarshalIndent(p, "", "    ")
	if err != nil {
		return nil, err
	}
	return append([]byte(xml.Header), append(out, '\n')...), nil
}

// numericVersion strips a leading "v" (e.g. "v60.0" -> "60.0") for the Metadata
// API, which expects the bare number.
func numericVersion(v string) string {
	return strings.TrimPrefix(v, "v")
}
