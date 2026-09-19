package source

import (
	"os"
	"path/filepath"
	"testing"
)

// setupNestedProject lays out a project whose files sit under custom feature
// module folders (not the standard force-app/main/default flat layout), the
// way sff's users in this codebase actually organize large projects.
func setupNestedProject(t *testing.T) (root string, dirs []string) {
	t.Helper()
	root = t.TempDir()
	files := []string{
		"source/authorization/src/classes/Foo.cls",
		"source/authorization/src/classes/Foo.cls-meta.xml",
		"source/authorization/src/objects/Account/fields/Bar__c.field-meta.xml",
		"source/calculatorService/src/lwc/calcWidget/calcWidget.js",
		"source/calculatorService/src/lwc/calcWidget/calcWidget.js-meta.xml",
	}
	for _, f := range files {
		p := filepath.Join(root, filepath.FromSlash(f))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return root, []string{filepath.Join(root, "source")}
}

func TestBuildPlacementIndexFindsNestedModuleFiles(t *testing.T) {
	root, dirs := setupNestedProject(t)
	index := BuildPlacementIndex(dirs, nil)

	cases := map[string]string{
		"classes/Foo.cls-meta.xml":                     "source/authorization/src/classes/Foo.cls-meta.xml",
		"objects/Account/fields/Bar__c.field-meta.xml": "source/authorization/src/objects/Account/fields/Bar__c.field-meta.xml",
		"lwc/calcWidget/calcWidget.js":                 "source/calculatorService/src/lwc/calcWidget/calcWidget.js",
	}
	for srcRel, wantSuffix := range cases {
		got, ok := index[srcRel]
		if !ok {
			t.Fatalf("index missing key %q; index=%v", srcRel, index)
		}
		want := filepath.Join(root, filepath.FromSlash(wantSuffix))
		if got != want {
			t.Errorf("index[%q] = %q, want %q", srcRel, got, want)
		}
	}
}
