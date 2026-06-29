package source

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ko4edikov/sff/pkg/mdapi"
	"github.com/ko4edikov/sff/pkg/project"
)

// writeProject lays out a minimal sfdx project (default package "force-app")
// populated with the given source files (paths under main/default) and returns it.
func writeProject(t *testing.T, files map[string]string) *project.Project {
	t.Helper()
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "sfdx-project.json"),
		[]byte(`{"packageDirectories":[{"path":"force-app","default":true}]}`), 0o644); err != nil {
		t.Fatal(err)
	}
	base := filepath.Join(root, "force-app", "main", "default")
	for rel, content := range files {
		p := filepath.Join(base, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	proj, err := project.Find(root)
	if err != nil {
		t.Fatal(err)
	}
	return proj
}

// TestRecomposeMembers resolves -m/-x members against a project (no catalog, so
// the fallbacks classify), exercising flat, decomposed, bundle, and static types
// plus a not-found warning.
func TestRecomposeMembers(t *testing.T) {
	proj := writeProject(t, map[string]string{
		"classes/Foo.cls":                                 "public class Foo {}",
		"classes/Foo.cls-meta.xml":                        metaXML("ApexClass"),
		"classes/Bar.cls":                                 "public class Bar {}",
		"classes/Bar.cls-meta.xml":                        metaXML("ApexClass"),
		"objects/Broker__c/Broker__c.object-meta.xml":     "<?xml version=\"1.0\" encoding=\"UTF-8\"?>\n<CustomObject xmlns=\"http://soap.sforce.com/2006/04/metadata\">\n    <label>Broker</label>\n</CustomObject>\n",
		"objects/Broker__c/fields/Name__c.field-meta.xml": "<?xml version=\"1.0\" encoding=\"UTF-8\"?>\n<CustomField xmlns=\"http://soap.sforce.com/2006/04/metadata\">\n    <fullName>Name__c</fullName>\n    <type>Text</type>\n    <length>80</length>\n</CustomField>\n",
		"lwc/myCmp/myCmp.js":                              "export default class {}",
		"lwc/myCmp/myCmp.js-meta.xml":                     metaXML("LightningComponentBundle"),
		"staticresources/Logo.png":                        "PNGDATA",
		"staticresources/Logo.resource-meta.xml":          "<?xml version=\"1.0\"?>\n<StaticResource xmlns=\"http://soap.sforce.com/2006/04/metadata\">\n<contentType>image/png</contentType>\n</StaticResource>\n",
	})

	pkg := &mdapi.Package{Types: []mdapi.PackageTypes{
		{Name: "ApexClass", Members: []string{"Foo"}},
		{Name: "CustomObject", Members: []string{"Broker__c"}},
		{Name: "LightningComponentBundle", Members: []string{"myCmp"}},
		{Name: "StaticResource", Members: []string{"Logo"}},
		{Name: "ApexClass", Members: []string{"Ghost"}}, // not in project
	}}

	rec, err := RecomposeMembers(proj, pkg, "60.0", nil)
	if err != nil {
		t.Fatalf("RecomposeMembers: %v", err)
	}

	// Only the requested ApexClass (Foo) is pulled in, not its sibling Bar.
	mustHave(t, rec, "classes/Foo.cls", "classes/Foo.cls-meta.xml")
	if _, ok := rec.Entries["classes/Bar.cls"]; ok {
		t.Error("Bar.cls leaked in: -m should select only the named member")
	}

	// Decomposed object is folded; bundle and static resource resolve too.
	if obj, ok := rec.Entries["objects/Broker__c.object"]; !ok {
		t.Error("missing recomposed object")
	} else if !bytes.Contains(obj, []byte("<fullName>Name__c</fullName>")) {
		t.Errorf("object did not fold field:\n%s", obj)
	}
	mustHave(t, rec, "lwc/myCmp/myCmp.js", "staticresources/Logo.resource", "staticresources/Logo.resource-meta.xml")

	// The missing member is a warning, not a failure.
	if !hasWarning(rec.Warnings, "ApexClass:Ghost") {
		t.Errorf("expected a not-found warning for Ghost, got %v", rec.Warnings)
	}
}

// TestRecomposeMembersWildcard selects every member of a type with "*".
func TestRecomposeMembersWildcard(t *testing.T) {
	proj := writeProject(t, map[string]string{
		"classes/Foo.cls":          "public class Foo {}",
		"classes/Foo.cls-meta.xml": metaXML("ApexClass"),
		"classes/Bar.cls":          "public class Bar {}",
		"classes/Bar.cls-meta.xml": metaXML("ApexClass"),
	})
	pkg := &mdapi.Package{Types: []mdapi.PackageTypes{{Name: "ApexClass", Members: []string{"*"}}}}

	rec, err := RecomposeMembers(proj, pkg, "60.0", nil)
	if err != nil {
		t.Fatalf("RecomposeMembers: %v", err)
	}
	mustHave(t, rec, "classes/Foo.cls", "classes/Bar.cls")
	var members []string
	for _, ty := range rec.Package.Types {
		if ty.Name == "ApexClass" {
			members = ty.Members
		}
	}
	if !equalStrings(members, []string{"Bar", "Foo"}) {
		t.Errorf("wildcard ApexClass members = %v, want [Bar Foo]", members)
	}
}

func hasWarning(warnings []string, sub string) bool {
	for _, w := range warnings {
		if strings.Contains(w, sub) {
			return true
		}
	}
	return false
}
