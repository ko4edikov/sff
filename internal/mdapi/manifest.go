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
	"lwc":   "LightningComponentBundle",
	"aura":  "AuraDefinitionBundle",
	"apex":  "ApexClass",
	"class": "ApexClass",
}

// resolveType returns the canonical Metadata API type name for a user-supplied
// type, applying case-insensitive aliases but otherwise passing it through.
func resolveType(t string) string {
	if canon, ok := typeAliases[strings.ToLower(t)]; ok {
		return canon
	}
	return t
}

// ParseSpecifiers builds a Package from "-m" values. Each spec is either
// "Type:Name" (a specific member) or a bare "Type" (wildcard "*"). Members are
// grouped by type and the type order is preserved by first appearance.
func ParseSpecifiers(specs []string, version string) (*Package, error) {
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
		typ, member := spec, "*"
		if i := strings.Index(spec, ":"); i >= 0 {
			typ, member = spec[:i], spec[i+1:]
		}
		typ = resolveType(strings.TrimSpace(typ))
		member = strings.TrimSpace(member)
		if typ == "" || member == "" {
			return nil, fmt.Errorf("invalid metadata specifier %q (want Type or Type:Name)", spec)
		}
		if _, seen := byType[typ]; !seen {
			order = append(order, typ)
		}
		byType[typ] = append(byType[typ], member)
	}

	pkg := &Package{Xmlns: metadataNS, Version: numericVersion(version)}
	for _, typ := range order {
		members := byType[typ]
		sort.Strings(members)
		pkg.Types = append(pkg.Types, PackageTypes{Members: members, Name: typ})
	}
	return pkg, nil
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
