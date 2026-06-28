package source

import (
	"archive/zip"
	"bytes"
	"os"
	"path/filepath"
	"testing"
)

// TestRecomposeDir walks a synthetic source tree (no describe catalog, so the
// built-in fallbacks classify everything) and checks the metadata-format entries
// and the generated manifest.
func TestRecomposeDir(t *testing.T) {
	root := t.TempDir()
	files := map[string]string{
		"classes/Foo.cls":                                 "public class Foo {}",
		"classes/Foo.cls-meta.xml":                        metaXML("ApexClass"),
		"objects/Broker__c/Broker__c.object-meta.xml":     "<?xml version=\"1.0\" encoding=\"UTF-8\"?>\n<CustomObject xmlns=\"http://soap.sforce.com/2006/04/metadata\">\n    <label>Broker</label>\n</CustomObject>\n",
		"objects/Broker__c/fields/Name__c.field-meta.xml": "<?xml version=\"1.0\" encoding=\"UTF-8\"?>\n<CustomField xmlns=\"http://soap.sforce.com/2006/04/metadata\">\n    <fullName>Name__c</fullName>\n    <type>Text</type>\n    <length>80</length>\n</CustomField>\n",
		"lwc/myCmp/myCmp.js":                              "export default class {}",
		"lwc/myCmp/myCmp.js-meta.xml":                     metaXML("LightningComponentBundle"),
		"lwc/myCmp/__tests__/myCmp.test.js":               "// ignored",
		"staticresources/Logo.png":                        "PNGDATA",
		"staticresources/Logo.resource-meta.xml":          "<?xml version=\"1.0\"?>\n<StaticResource xmlns=\"http://soap.sforce.com/2006/04/metadata\">\n<contentType>image/png</contentType>\n</StaticResource>\n",
		"staticresources/Assets/app.js":                   "console.log(1)",
		"staticresources/Assets/css/app.css":              "body{}",
		"staticresources/Assets.resource-meta.xml":        "<?xml version=\"1.0\"?>\n<StaticResource xmlns=\"http://soap.sforce.com/2006/04/metadata\">\n<contentType>application/zip</contentType>\n</StaticResource>\n",
	}
	for rel, content := range files {
		p := filepath.Join(root, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	rec, err := RecomposeDir(root, "60.0", nil)
	if err != nil {
		t.Fatalf("RecomposeDir: %v", err)
	}

	// Decomposed object folds the field back into one composed file.
	obj, ok := rec.Entries["objects/Broker__c.object"]
	if !ok {
		t.Fatal("missing recomposed objects/Broker__c.object")
	}
	if !bytes.Contains(obj, []byte("        <fullName>Name__c</fullName>")) {
		t.Errorf("object did not fold in field:\n%s", obj)
	}
	if _, leaked := rec.Entries["objects/Broker__c/Broker__c.object-meta.xml"]; leaked {
		t.Error("source-format object parts leaked into metadata entries")
	}

	// Apex class copies verbatim, both content and sidecar.
	mustHave(t, rec, "classes/Foo.cls", "classes/Foo.cls-meta.xml")

	// LWC bundle copies verbatim; the __tests__ file is excluded.
	mustHave(t, rec, "lwc/myCmp/myCmp.js", "lwc/myCmp/myCmp.js-meta.xml")
	if _, ok := rec.Entries["lwc/myCmp/__tests__/myCmp.test.js"]; ok {
		t.Error("bundle test file was not excluded")
	}

	// Single-file static resource becomes a .resource; the archive one is re-zipped.
	mustHave(t, rec, "staticresources/Logo.resource", "staticresources/Logo.resource-meta.xml")
	mustHave(t, rec, "staticresources/Assets.resource", "staticresources/Assets.resource-meta.xml")
	if got := string(rec.Entries["staticresources/Logo.resource"]); got != "PNGDATA" {
		t.Errorf("single static resource bytes = %q", got)
	}
	assertZipHas(t, rec.Entries["staticresources/Assets.resource"], "app.js", "css/app.css")

	// Manifest groups members by type.
	got := map[string][]string{}
	for _, ty := range rec.Package.Types {
		got[ty.Name] = ty.Members
	}
	wantMembers := map[string][]string{
		"ApexClass":                []string{"Foo"},
		"CustomObject":             []string{"Broker__c"},
		"LightningComponentBundle": []string{"myCmp"},
		"StaticResource":           []string{"Assets", "Logo"},
	}
	for typ, want := range wantMembers {
		if !equalStrings(got[typ], want) {
			t.Errorf("manifest %s members = %v, want %v", typ, got[typ], want)
		}
	}
	if rec.Package.Version != "60.0" {
		t.Errorf("manifest version = %q, want 60.0", rec.Package.Version)
	}
}

func metaXML(root string) string {
	return "<?xml version=\"1.0\" encoding=\"UTF-8\"?>\n<" + root + " xmlns=\"http://soap.sforce.com/2006/04/metadata\">\n    <apiVersion>60.0</apiVersion>\n</" + root + ">\n"
}

func mustHave(t *testing.T, rec *RecomposeResult, keys ...string) {
	t.Helper()
	for _, k := range keys {
		if _, ok := rec.Entries[k]; !ok {
			t.Errorf("missing metadata entry %s", k)
		}
	}
}

func assertZipHas(t *testing.T, data []byte, names ...string) {
	t.Helper()
	zr, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		t.Fatalf("open re-archived resource: %v", err)
	}
	have := map[string]bool{}
	for _, f := range zr.File {
		have[f.Name] = true
	}
	for _, n := range names {
		if !have[n] {
			t.Errorf("re-archived resource missing %s (has %v)", n, have)
		}
	}
}
