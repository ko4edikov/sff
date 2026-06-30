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
		name       string
		path       string
		wantType   string
		wantMember string
		wantScope  string
	}{
		{
			name:       "decomposed child field",
			path:       filepath.Join(base, "objects/Account/fields/Foo__c.field-meta.xml"),
			wantType:   "CustomObject",
			wantMember: "Account",
			wantScope:  "objects/Account/fields/Foo__c.field-meta.xml",
		},
		{
			name:       "decomposed parent file",
			path:       filepath.Join(base, "objects/Account/Account.object-meta.xml"),
			wantType:   "CustomObject",
			wantMember: "Account",
			wantScope:  "objects/Account/Account.object-meta.xml",
		},
		{
			name:       "object directory diffs whole subtree",
			path:       filepath.Join(base, "objects/Account"),
			wantType:   "CustomObject",
			wantMember: "Account",
			wantScope:  "",
		},
		{
			name:       "non-decomposed layout",
			path:       filepath.Join(base, "layouts/Account-Account Layout.layout-meta.xml"),
			wantType:   "Layout",
			wantMember: "Account-Account Layout",
			wantScope:  "layouts/Account-Account Layout.layout-meta.xml",
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
			if got.RetrieveType != tc.wantType {
				t.Errorf("RetrieveType = %q, want %q", got.RetrieveType, tc.wantType)
			}
			if got.RetrieveMember != tc.wantMember {
				t.Errorf("RetrieveMember = %q, want %q", got.RetrieveMember, tc.wantMember)
			}
			if got.ScopeRel != tc.wantScope {
				t.Errorf("ScopeRel = %q, want %q", got.ScopeRel, tc.wantScope)
			}
		})
	}
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
