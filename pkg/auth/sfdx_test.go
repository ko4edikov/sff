package auth

import (
	"os"
	"path/filepath"
	"testing"
)

// TestDefaultTargetLocalConfig verifies a project-local .sf/config.json is used
// for the default org (matching `sf config set target-org` with Location:Local),
// including when sff runs from a nested subdirectory.
func TestDefaultTargetLocalConfig(t *testing.T) {
	root := t.TempDir()
	sfDir := filepath.Join(root, ".sf")
	if err := os.MkdirAll(sfDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(sfDir, "config.json"), []byte(`{"target-org":"neo-dev"}`), 0o644); err != nil {
		t.Fatal(err)
	}

	nested := filepath.Join(root, "force-app", "main", "default")
	if err := os.MkdirAll(nested, 0o755); err != nil {
		t.Fatal(err)
	}
	t.Chdir(nested)

	got, err := defaultTarget()
	if err != nil {
		t.Fatalf("defaultTarget: %v", err)
	}
	if got != "neo-dev" {
		t.Errorf("defaultTarget = %q, want %q", got, "neo-dev")
	}
}

// TestDefaultTargetLegacyLocalConfig verifies the legacy
// .sfdx/sfdx-config.json (defaultusername) is honored when no .sf config exists.
func TestDefaultTargetLegacyLocalConfig(t *testing.T) {
	root := t.TempDir()
	sfdxDir := filepath.Join(root, ".sfdx")
	if err := os.MkdirAll(sfdxDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(sfdxDir, "sfdx-config.json"), []byte(`{"defaultusername":"legacy@org"}`), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Chdir(root)

	got, err := defaultTarget()
	if err != nil {
		t.Fatalf("defaultTarget: %v", err)
	}
	if got != "legacy@org" {
		t.Errorf("defaultTarget = %q, want %q", got, "legacy@org")
	}
}
