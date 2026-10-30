package source

import (
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"testing"

	"github.com/ko4edikov/sff/pkg/mdapi"
	"github.com/ko4edikov/sff/pkg/project"
)

// deleteFixture lays out a small sfdx project and returns it with its default
// metadata root.
func deleteFixture(t *testing.T, files ...string) (*project.Project, string) {
	t.Helper()
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "sfdx-project.json"),
		[]byte(`{"packageDirectories":[{"path":"force-app","default":true}]}`), 0o644); err != nil {
		t.Fatal(err)
	}
	base := filepath.Join(root, "force-app", "main", "default")
	for _, f := range files {
		p := filepath.Join(base, filepath.FromSlash(f))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	proj, err := project.Find(root)
	if err != nil {
		t.Fatal(err)
	}
	return proj, base
}

// planMembers flattens a plan's manifest to sorted "Type:Member" strings.
func planMembers(p *DeletePlan) []string {
	var out []string
	for _, ty := range p.Package.Types {
		for _, m := range ty.Members {
			out = append(out, ty.Name+":"+m)
		}
	}
	sort.Strings(out)
	return out
}

// planFiles returns the plan's files relative to base.
func planFiles(t *testing.T, p *DeletePlan, base string) []string {
	t.Helper()
	var out []string
	for _, f := range p.Files {
		rel, err := filepath.Rel(base, f)
		if err != nil {
			t.Fatal(err)
		}
		out = append(out, filepath.ToSlash(rel))
	}
	return out
}

func TestResolveDeleteFlatFilePullsSidecar(t *testing.T) {
	proj, base := deleteFixture(t, "classes/Foo.cls", "classes/Foo.cls-meta.xml", "classes/Bar.cls", "classes/Bar.cls-meta.xml")
	plan, err := ResolveDelete(proj, []string{filepath.Join(base, "classes", "Foo.cls")}, nil, "v62.0", nil)
	if err != nil {
		t.Fatal(err)
	}
	if got, want := planMembers(plan), []string{"ApexClass:Foo"}; !reflect.DeepEqual(got, want) {
		t.Errorf("members = %v, want %v", got, want)
	}
	if got, want := planFiles(t, plan, base), []string{"classes/Foo.cls", "classes/Foo.cls-meta.xml"}; !reflect.DeepEqual(got, want) {
		t.Errorf("files = %v, want %v", got, want)
	}
	if plan.Package.Version != "62.0" {
		t.Errorf("version = %q, want 62.0", plan.Package.Version)
	}
}

func TestResolveDeleteFieldFileIsNarrow(t *testing.T) {
	proj, base := deleteFixture(t,
		"objects/Account/Account.object-meta.xml",
		"objects/Account/fields/X__c.field-meta.xml",
		"objects/Account/fields/Y__c.field-meta.xml")
	plan, err := ResolveDelete(proj, []string{filepath.Join(base, "objects/Account/fields/X__c.field-meta.xml")}, nil, "62.0", nil)
	if err != nil {
		t.Fatal(err)
	}
	if got, want := planMembers(plan), []string{"CustomField:Account.X__c"}; !reflect.DeepEqual(got, want) {
		t.Errorf("members = %v, want %v", got, want)
	}
	if got, want := planFiles(t, plan, base), []string{"objects/Account/fields/X__c.field-meta.xml"}; !reflect.DeepEqual(got, want) {
		t.Errorf("files = %v, want %v", got, want)
	}
}

func TestResolveDeleteObjectDirSubsumesChildren(t *testing.T) {
	proj, base := deleteFixture(t,
		"objects/Account/Account.object-meta.xml",
		"objects/Account/fields/X__c.field-meta.xml",
		"objects/Other__c/Other__c.object-meta.xml")
	plan, err := ResolveDelete(proj, []string{filepath.Join(base, "objects/Account")}, nil, "62.0", nil)
	if err != nil {
		t.Fatal(err)
	}
	if got, want := planMembers(plan), []string{"CustomObject:Account"}; !reflect.DeepEqual(got, want) {
		t.Errorf("members = %v, want %v", got, want)
	}
	if got, want := len(plan.Files), 2; got != want {
		t.Errorf("files = %v, want %d entries", planFiles(t, plan, base), want)
	}
}

func TestResolveDeleteBundleAndStaticResource(t *testing.T) {
	proj, base := deleteFixture(t,
		"lwc/cmp/cmp.js", "lwc/cmp/cmp.html", "lwc/cmp/cmp.js-meta.xml",
		"staticresources/Lib.resource-meta.xml", "staticresources/Lib/a.js", "staticresources/Lib/sub/b.js")
	plan, err := ResolveDelete(proj, []string{
		filepath.Join(base, "lwc/cmp/cmp.html"),
		filepath.Join(base, "staticresources/Lib/sub/b.js"),
	}, nil, "62.0", nil)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"LightningComponentBundle:cmp", "StaticResource:Lib"}
	if got := planMembers(plan); !reflect.DeepEqual(got, want) {
		t.Errorf("members = %v, want %v", got, want)
	}
	if got := len(plan.Files); got != 6 {
		t.Errorf("files = %v, want 6 entries", planFiles(t, plan, base))
	}
}

func TestResolveDeleteMetadataWildcard(t *testing.T) {
	proj, base := deleteFixture(t, "classes/A.cls", "classes/A.cls-meta.xml", "classes/B.cls", "classes/B.cls-meta.xml")
	pkg := &mdapi.Package{Types: []mdapi.PackageTypes{{Name: "ApexClass", Members: []string{"*"}}, {Name: "ApexClass", Members: []string{"Missing"}}}}
	plan, err := ResolveDelete(proj, nil, pkg, "62.0", nil)
	if err != nil {
		t.Fatal(err)
	}
	if got, want := planMembers(plan), []string{"ApexClass:A", "ApexClass:B"}; !reflect.DeepEqual(got, want) {
		t.Errorf("members = %v, want %v", got, want)
	}
	if len(plan.Files) != 4 {
		t.Errorf("files = %v, want 4 entries", planFiles(t, plan, base))
	}
	if len(plan.Warnings) != 1 || !strings.Contains(plan.Warnings[0], "ApexClass:Missing") {
		t.Errorf("warnings = %v, want one for ApexClass:Missing", plan.Warnings)
	}
}

func TestResolveDeleteNothingFound(t *testing.T) {
	proj, _ := deleteFixture(t, "classes/A.cls")
	pkg := &mdapi.Package{Types: []mdapi.PackageTypes{{Name: "ApexClass", Members: []string{"Nope"}}}}
	if _, err := ResolveDelete(proj, nil, pkg, "62.0", nil); err == nil {
		t.Fatal("want error when no component has local source")
	}
}

func TestRemoveLocalPrunesEmptyDirsButKeepsTypeDir(t *testing.T) {
	proj, base := deleteFixture(t, "lwc/cmp/cmp.js", "lwc/cmp/__tests__/cmp.test.js", "classes/Keep.cls")
	plan, err := ResolveDelete(proj, []string{filepath.Join(base, "lwc/cmp")}, nil, "62.0", nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := plan.RemoveLocal(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(base, "lwc/cmp")); !os.IsNotExist(err) {
		t.Errorf("lwc/cmp should be gone, stat err = %v", err)
	}
	if _, err := os.Stat(filepath.Join(base, "lwc")); err != nil {
		t.Errorf("lwc type dir should stay: %v", err)
	}
	if _, err := os.Stat(filepath.Join(base, "classes/Keep.cls")); err != nil {
		t.Errorf("unrelated file removed: %v", err)
	}
}

const labelsFixture = `<?xml version="1.0" encoding="UTF-8"?>
<CustomLabels xmlns="http://soap.sforce.com/2006/04/metadata">
    <labels>
        <fullName>One</fullName>
        <value>1</value>
    </labels>
    <labels>
        <fullName>Two</fullName>
        <value>2</value>
    </labels>
</CustomLabels>
`

func TestResolveDeleteCustomLabelEditsFile(t *testing.T) {
	proj, base := deleteFixture(t)
	path := filepath.Join(base, "labels", "CustomLabels.labels-meta.xml")
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(labelsFixture), 0o644); err != nil {
		t.Fatal(err)
	}
	pkg := &mdapi.Package{Types: []mdapi.PackageTypes{{Name: "CustomLabel", Members: []string{"One", "Nope"}}}}
	plan, err := ResolveDelete(proj, nil, pkg, "62.0", nil)
	if err != nil {
		t.Fatal(err)
	}
	if got, want := planMembers(plan), []string{"CustomLabel:One"}; !reflect.DeepEqual(got, want) {
		t.Errorf("members = %v, want %v", got, want)
	}
	if len(plan.Files) != 0 || len(plan.Warnings) != 1 {
		t.Errorf("files = %v, warnings = %v; want no files and one warning", plan.Files, plan.Warnings)
	}
	if err := plan.RemoveLocal(); err != nil {
		t.Fatal(err)
	}
	got, _ := os.ReadFile(path)
	if strings.Contains(string(got), "One") || !strings.Contains(string(got), "<fullName>Two</fullName>") {
		t.Errorf("labels file after delete:\n%s", got)
	}

	all := &mdapi.Package{Types: []mdapi.PackageTypes{{Name: "CustomLabel", Members: []string{"*"}}}}
	plan, err = ResolveDelete(proj, nil, all, "62.0", nil)
	if err != nil {
		t.Fatal(err)
	}
	if got, want := planMembers(plan), []string{"CustomLabel:Two"}; !reflect.DeepEqual(got, want) {
		t.Errorf("wildcard members = %v, want %v", got, want)
	}
}

func TestResolveDeleteMetadataGlob(t *testing.T) {
	proj, _ := deleteFixture(t,
		"classes/EUR_ScoreA.cls", "classes/EUR_ScoreA.cls-meta.xml",
		"classes/eur_scoreB.cls", "classes/eur_scoreB.cls-meta.xml",
		"classes/Other.cls", "classes/Other.cls-meta.xml")
	pkg := &mdapi.Package{Types: []mdapi.PackageTypes{{Name: "ApexClass", Members: []string{"EUR_Score*", "Zzz*"}}}}
	plan, err := ResolveDelete(proj, nil, pkg, "62.0", nil)
	if err != nil {
		t.Fatal(err)
	}
	if got, want := planMembers(plan), []string{"ApexClass:EUR_ScoreA", "ApexClass:eur_scoreB"}; !reflect.DeepEqual(got, want) {
		t.Errorf("members = %v, want %v", got, want)
	}
	if len(plan.Files) != 4 {
		t.Errorf("files = %d, want 4", len(plan.Files))
	}
	if len(plan.Warnings) != 1 || !strings.Contains(plan.Warnings[0], "Zzz*") {
		t.Errorf("warnings = %v, want one for Zzz*", plan.Warnings)
	}
}
