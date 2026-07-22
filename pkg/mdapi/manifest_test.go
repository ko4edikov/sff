package mdapi

import (
	"os"
	"testing"
)

// membersOf returns the members recorded for a type name (canonical), or nil.
func membersOf(p *Package, name string) []string {
	for _, t := range p.Types {
		if t.Name == name {
			return t.Members
		}
	}
	return nil
}

func TestParseSpecifiersCommaList(t *testing.T) {
	pkg, err := ParseSpecifiers([]string{"PermissionSet:pm1,pm2, pm3"}, "60.0", nil)
	if err != nil {
		t.Fatalf("ParseSpecifiers: %v", err)
	}
	got := membersOf(pkg, "PermissionSet")
	want := []string{"pm1", "pm2", "pm3"} // sorted
	if len(got) != len(want) {
		t.Fatalf("members = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("members = %v, want %v", got, want)
		}
	}
}

func TestParseSpecifiersCaseInsensitive(t *testing.T) {
	catalog := &DescribeResult{Objects: []MetadataObject{
		{Name: "PermissionSet"},
		{Name: "ApexClass"},
	}}
	r := NewTypeResolver(catalog)

	pkg, err := ParseSpecifiers([]string{"permissionset:Admin", "APEXCLASS:Svc"}, "60.0", r)
	if err != nil {
		t.Fatalf("ParseSpecifiers: %v", err)
	}
	if m := membersOf(pkg, "PermissionSet"); len(m) != 1 || m[0] != "Admin" {
		t.Fatalf("PermissionSet members = %v", m)
	}
	if m := membersOf(pkg, "ApexClass"); len(m) != 1 || m[0] != "Svc" {
		t.Fatalf("ApexClass members = %v", m)
	}
}

func TestParseSpecifiersAliasesWithoutCatalog(t *testing.T) {
	pkg, err := ParseSpecifiers([]string{"lwc:myCmp", "class"}, "60.0", nil)
	if err != nil {
		t.Fatalf("ParseSpecifiers: %v", err)
	}
	if m := membersOf(pkg, "LightningComponentBundle"); len(m) != 1 || m[0] != "myCmp" {
		t.Fatalf("lwc alias not resolved: %v", pkg.Types)
	}
	if m := membersOf(pkg, "ApexClass"); len(m) != 1 || m[0] != "*" {
		t.Fatalf("class alias / wildcard not resolved: %v", pkg.Types)
	}
}

func TestParseSpecifiersLabelAlias(t *testing.T) {
	pkg, err := ParseSpecifiers([]string{"label:Greeting"}, "60.0", nil)
	if err != nil {
		t.Fatalf("ParseSpecifiers: %v", err)
	}
	if m := membersOf(pkg, "CustomLabel"); len(m) != 1 || m[0] != "Greeting" {
		t.Fatalf("label alias not resolved: %v", pkg.Types)
	}
}

func TestParseSpecifiersMoreAliases(t *testing.T) {
	pkg, err := ParseSpecifiers([]string{"object:Account", "page:MyPage", "trigger:MyTrigger"}, "60.0", nil)
	if err != nil {
		t.Fatalf("ParseSpecifiers: %v", err)
	}
	if m := membersOf(pkg, "CustomObject"); len(m) != 1 || m[0] != "Account" {
		t.Fatalf("object alias not resolved: %v", pkg.Types)
	}
	if m := membersOf(pkg, "ApexPage"); len(m) != 1 || m[0] != "MyPage" {
		t.Fatalf("page alias not resolved: %v", pkg.Types)
	}
	if m := membersOf(pkg, "ApexTrigger"); len(m) != 1 || m[0] != "MyTrigger" {
		t.Fatalf("trigger alias not resolved: %v", pkg.Types)
	}
}

func TestNewTypeResolverExtraNames(t *testing.T) {
	r := NewTypeResolver(nil, "StaticResource", "ApexTrigger")
	if got := r.Resolve("staticresource"); got != "StaticResource" {
		t.Fatalf("Resolve(staticresource) = %q", got)
	}
	if got := r.Resolve("APEXTRIGGER"); got != "ApexTrigger" {
		t.Fatalf("Resolve(APEXTRIGGER) = %q", got)
	}
	// aliases still work alongside extras
	if got := r.Resolve("class"); got != "ApexClass" {
		t.Fatalf("Resolve(class) = %q", got)
	}
	// unknown passes through unchanged
	if got := r.Resolve("Whatever"); got != "Whatever" {
		t.Fatalf("Resolve(Whatever) = %q", got)
	}
}

func TestBuildPackageManifestTakesPrecedence(t *testing.T) {
	dir := t.TempDir()
	path := dir + "/package.xml"
	xml := `<?xml version="1.0" encoding="UTF-8"?>
<Package xmlns="http://soap.sforce.com/2006/04/metadata">
  <types><members>Existing</members><name>ApexClass</name></types>
  <version>60.0</version>
</Package>`
	if err := os.WriteFile(path, []byte(xml), 0o644); err != nil {
		t.Fatal(err)
	}
	// specs are ignored when a manifest is given
	pkg, err := BuildPackage(path, []string{"PermissionSet:Admin"}, "60.0", nil)
	if err != nil {
		t.Fatalf("BuildPackage: %v", err)
	}
	if m := membersOf(pkg, "ApexClass"); len(m) != 1 || m[0] != "Existing" {
		t.Fatalf("manifest not loaded: %v", pkg.Types)
	}
	if membersOf(pkg, "PermissionSet") != nil {
		t.Fatal("specs should be ignored when manifest is set")
	}
}

func TestBuildPackageFallsBackToSpecs(t *testing.T) {
	pkg, err := BuildPackage("", []string{"apexclass:A,B"}, "60.0", NewTypeResolver(nil, "ApexClass"))
	if err != nil {
		t.Fatalf("BuildPackage: %v", err)
	}
	if m := membersOf(pkg, "ApexClass"); len(m) != 2 || m[0] != "A" || m[1] != "B" {
		t.Fatalf("specs not parsed: %v", pkg.Types)
	}
}

func TestParseSpecifiersEmptyMember(t *testing.T) {
	if _, err := ParseSpecifiers([]string{"ApexClass:"}, "60.0", nil); err == nil {
		t.Fatal("expected error for empty member")
	}
	if _, err := ParseSpecifiers([]string{"ApexClass:a,,b"}, "60.0", nil); err == nil {
		t.Fatal("expected error for empty member in list")
	}
}
