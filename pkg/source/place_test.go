package source

import (
	"path/filepath"
	"testing"

	"github.com/ko4edikov/sff/pkg/project"
)

// TestPlaceInProjectDestOverride verifies that an explicit dest root bypasses
// project merging: files land under dest/<srcRel> regardless of an existing
// copy in a package directory.
func TestPlaceInProjectDestOverride(t *testing.T) {
	proj, _ := setupProject(t)
	rel := "classes/MyClass.cls-meta.xml"

	got := placeInProject(proj, filepath.Join("out", "sub"), rel, nil)
	want := filepath.Join("out", "sub", filepath.FromSlash(rel))
	if got != want {
		t.Fatalf("dest override = %q, want %q", got, want)
	}
}

// TestPlaceInProjectDefaultDir verifies that without a dest, a new (non-existing)
// file lands under the default package directory's main/default tree.
func TestPlaceInProjectDefaultDir(t *testing.T) {
	proj, base := setupProject(t) // base = <root>/force-app/main/default
	rel := "classes/New.cls"

	got := placeInProject(proj, "", rel, nil)
	want := filepath.Join(base, filepath.FromSlash(rel))
	if got != want {
		t.Fatalf("default placement = %q, want %q", got, want)
	}
}

// TestPlaceInProjectUsesIndexForNestedModules verifies that when an index is
// supplied, an existing file under a custom module folder is matched and
// overwritten in place instead of falling back to the default package dir.
func TestPlaceInProjectUsesIndexForNestedModules(t *testing.T) {
	root, dirs := setupNestedProject(t)
	proj := &project.Project{Root: root, Dirs: []project.Dir{{Path: "source", Default: true}}}
	index := BuildPlacementIndex(dirs, nil)

	got := placeInProject(proj, "", "classes/Foo.cls-meta.xml", index)
	want := filepath.Join(root, "source", "authorization", "src", "classes", "Foo.cls-meta.xml")
	if got != want {
		t.Fatalf("placeInProject with index = %q, want %q", got, want)
	}
}
