package mdapi

import (
	"os"
	"path/filepath"
	"testing"
)

func TestLoadMetadataDir(t *testing.T) {
	dir := t.TempDir()
	pkgXML := `<?xml version="1.0" encoding="UTF-8"?>
<Package xmlns="http://soap.sforce.com/2006/04/metadata">
    <types>
        <members>MyClass</members>
        <name>ApexClass</name>
    </types>
    <version>60.0</version>
</Package>`
	writeFile(t, filepath.Join(dir, "package.xml"), pkgXML)
	writeFile(t, filepath.Join(dir, "classes", "MyClass.cls"), "public class MyClass {}")
	writeFile(t, filepath.Join(dir, "classes", "MyClass.cls-meta.xml"), "<meta/>")

	entries, pkg, err := LoadMetadataDir(dir)
	if err != nil {
		t.Fatalf("LoadMetadataDir: %v", err)
	}

	// Every file, including package.xml, is kept verbatim under a forward-slash path.
	want := []string{"package.xml", "classes/MyClass.cls", "classes/MyClass.cls-meta.xml"}
	for _, name := range want {
		if _, ok := entries[name]; !ok {
			t.Errorf("missing entry %q (have %v)", name, keys(entries))
		}
	}
	if len(entries) != len(want) {
		t.Errorf("got %d entries, want %d: %v", len(entries), len(want), keys(entries))
	}
	if got := string(entries["classes/MyClass.cls"]); got != "public class MyClass {}" {
		t.Errorf("class body = %q", got)
	}

	// The root package.xml is parsed into the manifest.
	if pkg.Version != "60.0" {
		t.Errorf("version = %q, want 60.0", pkg.Version)
	}
	if len(pkg.Types) != 1 || pkg.Types[0].Name != "ApexClass" || pkg.Types[0].Members[0] != "MyClass" {
		t.Errorf("types = %+v", pkg.Types)
	}
	if pkg.Xmlns != metadataNS {
		t.Errorf("xmlns = %q", pkg.Xmlns)
	}
}

func TestLoadMetadataDir_NoPackageXML(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "classes", "MyClass.cls"), "public class MyClass {}")

	if _, _, err := LoadMetadataDir(dir); err == nil {
		t.Fatal("want error for missing package.xml, got nil")
	}
}

func TestLoadMetadataDir_NotADirectory(t *testing.T) {
	dir := t.TempDir()
	file := filepath.Join(dir, "package.xml")
	writeFile(t, file, "<Package/>")

	if _, _, err := LoadMetadataDir(file); err == nil {
		t.Fatal("want error for a file path, got nil")
	}
}

func writeFile(t *testing.T, path, body string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

func keys(m map[string][]byte) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}
