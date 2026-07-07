package mdapi

import "testing"

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

func TestParseSpecifiersEmptyMember(t *testing.T) {
	if _, err := ParseSpecifiers([]string{"ApexClass:"}, "60.0", nil); err == nil {
		t.Fatal("expected error for empty member")
	}
	if _, err := ParseSpecifiers([]string{"ApexClass:a,,b"}, "60.0", nil); err == nil {
		t.Fatal("expected error for empty member in list")
	}
}
