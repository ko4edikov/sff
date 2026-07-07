package source

import (
	"path/filepath"
	"testing"
)

// TestPlaceInProjectDestOverride verifies that an explicit dest root bypasses
// project merging: files land under dest/<srcRel> regardless of an existing
// copy in a package directory.
func TestPlaceInProjectDestOverride(t *testing.T) {
	proj, _ := setupProject(t)
	rel := "classes/MyClass.cls-meta.xml"

	got := placeInProject(proj, filepath.Join("out", "sub"), rel)
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

	got := placeInProject(proj, "", rel)
	want := filepath.Join(base, filepath.FromSlash(rel))
	if got != want {
		t.Fatalf("default placement = %q, want %q", got, want)
	}
}
