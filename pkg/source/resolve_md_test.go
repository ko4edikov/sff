package source

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/ko4edikov/sff/pkg/mdapi"
	"github.com/ko4edikov/sff/pkg/project"
)

// setupProject lays out a minimal sfdx project with a decomposed object and a
// non-decomposed layout, returning the project rooted at the temp dir.
func setupProject(t *testing.T) (*project.Project, string) {
	t.Helper()
	root := t.TempDir()
	base := filepath.Join(root, "force-app", "main", "default")
	files := []string{
		"objects/Account/Account.object-meta.xml",
		"objects/Account/fields/Foo__c.field-meta.xml",
		"layouts/Account-Account Layout.layout-meta.xml",
	}
	for _, f := range files {
		p := filepath.Join(base, filepath.FromSlash(f))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte("<x/>"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return &project.Project{Root: root, Dirs: []project.Dir{{Path: "force-app", Default: true}}}, base
}

func TestResolveRetrieve(t *testing.T) {
	proj, base := setupProject(t)
	catalog := &mdapi.DescribeResult{Objects: []mdapi.MetadataObject{
		{Name: "Layout", DirectoryName: "layouts", Suffix: "layout"},
	}}

	cases := []struct {
		name      string
		path      string
		wantSpecs []string
		wantScope string
	}{
		{
			name:      "decomposed child field",
			path:      filepath.Join(base, "objects/Account/fields/Foo__c.field-meta.xml"),
			wantSpecs: []string{"CustomObject:Account"},
			wantScope: "objects/Account/fields/Foo__c.field-meta.xml",
		},
		{
			name:      "decomposed parent file",
			path:      filepath.Join(base, "objects/Account/Account.object-meta.xml"),
			wantSpecs: []string{"CustomObject:Account"},
			wantScope: "objects/Account/Account.object-meta.xml",
		},
		{
			name:      "object directory diffs whole subtree",
			path:      filepath.Join(base, "objects/Account"),
			wantSpecs: []string{"CustomObject:Account"},
			wantScope: "objects/Account",
		},
		{
			name:      "fields subfolder scopes to the fields subtree",
			path:      filepath.Join(base, "objects/Account/fields"),
			wantSpecs: []string{"CustomObject:Account"},
			wantScope: "objects/Account/fields",
		},
		{
			name:      "non-decomposed layout",
			path:      filepath.Join(base, "layouts/Account-Account Layout.layout-meta.xml"),
			wantSpecs: []string{"Layout:Account-Account Layout"},
			wantScope: "layouts/Account-Account Layout.layout-meta.xml",
		},
		{
			name:      "type container enumerates all components",
			path:      filepath.Join(base, "objects"),
			wantSpecs: []string{"CustomObject:Account"},
			wantScope: "objects",
		},
		{
			name:      "whole project: all types, empty scope",
			path:      base,
			wantSpecs: []string{"CustomObject:Account", "Layout:Account-Account Layout"},
			wantScope: "",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := ResolveRetrieve(tc.path, proj, catalog)
			if err != nil {
				t.Fatalf("ResolveRetrieve: %v", err)
			}
			if got.Kind != Retrieve {
				t.Errorf("Kind = %v, want Retrieve", got.Kind)
			}
			if !equalUnordered(got.RetrieveSpecs, tc.wantSpecs) {
				t.Errorf("RetrieveSpecs = %v, want %v", got.RetrieveSpecs, tc.wantSpecs)
			}
			if got.ScopeRel != tc.wantScope {
				t.Errorf("ScopeRel = %q, want %q", got.ScopeRel, tc.wantScope)
			}
		})
	}
}

// equalUnordered reports whether a and b contain the same elements (set equality;
// enumeration order is not significant).
func equalUnordered(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	seen := map[string]int{}
	for _, s := range a {
		seen[s]++
	}
	for _, s := range b {
		seen[s]--
	}
	for _, n := range seen {
		if n != 0 {
			return false
		}
	}
	return true
}

func TestResolveRetrieveUnknownDir(t *testing.T) {
	proj, base := setupProject(t)
	catalog := &mdapi.DescribeResult{} // empty: no directory→type mapping
	p := filepath.Join(base, "layouts/Account-Account Layout.layout-meta.xml")
	if _, err := ResolveRetrieve(p, proj, catalog); err == nil {
		t.Fatal("expected error for unknown metadata directory, got nil")
	}
}

func TestResolveRetrieveOutsideProject(t *testing.T) {
	proj, _ := setupProject(t)
	if _, err := ResolveRetrieve(t.TempDir(), proj, &mdapi.DescribeResult{}); err == nil {
		t.Fatal("expected error for path outside the project, got nil")
	}
}
