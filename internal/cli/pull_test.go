package cli

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

func initGitRepo(t *testing.T, dir string) {
	t.Helper()
	run := func(args ...string) {
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	run("init", "-q")
	run("config", "user.email", "test@example.com")
	run("config", "user.name", "Test")
}

func TestCheckGitCleanRejectsDirtyTree(t *testing.T) {
	dir := t.TempDir()
	initGitRepo(t, dir)
	if err := os.WriteFile(filepath.Join(dir, "a.txt"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}

	err := checkGitClean(dir, false)
	if err == nil {
		t.Fatal("expected an error for a dirty (untracked-file) working tree")
	}
}

func TestCheckGitCleanAllowsForce(t *testing.T) {
	dir := t.TempDir()
	initGitRepo(t, dir)
	if err := os.WriteFile(filepath.Join(dir, "a.txt"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}

	if err := checkGitClean(dir, true); err != nil {
		t.Fatalf("checkGitClean with force=true: %v", err)
	}
}

func TestCheckGitCleanAllowsCleanTree(t *testing.T) {
	dir := t.TempDir()
	initGitRepo(t, dir)

	if err := checkGitClean(dir, false); err != nil {
		t.Fatalf("checkGitClean on a clean tree: %v", err)
	}
}
