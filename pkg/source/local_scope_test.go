package source

import (
	"sort"
	"testing"
)

func TestLocalScopeNestedModules(t *testing.T) {
	_, dirs := setupNestedProject(t) // from place_index_test.go

	got := LocalScope(dirs, nil)
	sort.Strings(got)

	want := []string{
		"ApexClass:Foo",
		"CustomObject:Account",
		"LightningComponentBundle:calcWidget",
	}
	sort.Strings(want)

	if len(got) != len(want) {
		t.Fatalf("LocalScope = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("LocalScope = %v, want %v", got, want)
		}
	}
}

func TestLocalScopeTopLevelType(t *testing.T) {
	root, dirs := setupNestedProject(t)
	if err := writeFile(root+"/source/authorization/src/labels/CustomLabels.labels-meta.xml", []byte("<x/>")); err != nil {
		t.Fatal(err)
	}

	got := LocalScope(dirs, nil)
	found := false
	for _, s := range got {
		if s == "CustomLabels" {
			found = true
		}
	}
	if !found {
		t.Fatalf("LocalScope = %v, want it to include bare %q for a topLevel type", got, "CustomLabels")
	}
}
