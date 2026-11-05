package source

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/ko4edikov/sff/pkg/project"
)

func TestResolveChanges(t *testing.T) {
	root := t.TempDir()
	base := filepath.Join(root, "force-app", "main", "default")
	touch := func(rel string) string {
		p := filepath.Join(base, rel)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
		return p
	}
	gone := func(rel string) string { return filepath.Join(base, rel) }

	proj := &project.Project{Root: root, Dirs: []project.Dir{{Path: "force-app", Default: true}}}

	files := []ChangedFile{
		{Path: touch("classes/Mod.cls")},                         // modified class
		{Path: gone("classes/Old.cls"), Deleted: true},           // deleted class...
		{Path: gone("classes/Old.cls-meta.xml"), Deleted: true},  // ...with its meta
		{Path: gone("classes/Half.cls-meta.xml"), Deleted: true}, // only meta deleted
		{Path: touch("classes/Half.cls")},                        // body still there
		{Path: gone("lwc/gone/gone.js"), Deleted: true},          // whole bundle deleted
		{Path: gone("lwc/part/part.css"), Deleted: true},         // one bundle file deleted
		{Path: touch("lwc/part/part.js")},                        // bundle remains
		{Path: gone("objects/Account/fields/Legacy__c.field-meta.xml"), Deleted: true},
		{Path: touch("objects/Account/fields/Fresh__c.field-meta.xml")},
		{Path: filepath.Join(root, "README.md")}, // outside type dirs
	}
	cs, err := ResolveChanges(proj, files, "v62.0", nil)
	if err != nil {
		t.Fatal(err)
	}

	got := map[string]map[string][]string{"deploy": {}, "destroy": {}}
	for _, ty := range cs.Deploy.Types {
		got["deploy"][ty.Name] = ty.Members
	}
	for _, ty := range cs.Destroy.Types {
		got["destroy"][ty.Name] = ty.Members
	}
	want := map[string]map[string][]string{
		"deploy": {
			"ApexClass":                {"Half", "Mod"},
			"LightningComponentBundle": {"part"},
			"CustomField":              {"Account.Fresh__c"},
		},
		"destroy": {
			"ApexClass":                {"Old"},
			"LightningComponentBundle": {"gone"},
			"CustomField":              {"Account.Legacy__c"},
		},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got  %v\nwant %v", got, want)
	}
	wantPaths := []string{
		filepath.Join(base, "classes", "Half.cls"),
		filepath.Join(base, "classes", "Mod.cls"),
		filepath.Join(base, "lwc", "part"),
		filepath.Join(base, "objects", "Account", "fields", "Fresh__c.field-meta.xml"),
	}
	if !reflect.DeepEqual(cs.Paths, wantPaths) {
		t.Fatalf("paths = %v\nwant   %v", cs.Paths, wantPaths)
	}
	if cs.Deploy.Version != "62.0" {
		t.Fatalf("version = %q", cs.Deploy.Version)
	}
}
