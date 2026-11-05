package gitchange

import (
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"testing"
)

func run(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
}

func write(t *testing.T, dir, rel, body string) {
	t.Helper()
	p := filepath.Join(dir, rel)
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

func newRepo(t *testing.T) string {
	t.Helper()
	dir, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	run(t, dir, "init", "-q")
	run(t, dir, "config", "user.email", "t@example.com")
	run(t, dir, "config", "user.name", "T")
	run(t, dir, "config", "core.autocrlf", "false")
	return dir
}

// summarize renders files as sorted "+rel" / "-rel" lines.
func summarize(dir string, files []File) string {
	var out []string
	for _, f := range files {
		rel, _ := filepath.Rel(dir, f.Path)
		sign := "+"
		if f.Deleted {
			sign = "-"
		}
		out = append(out, sign+filepath.ToSlash(rel))
	}
	sort.Strings(out)
	return strings.Join(out, " ")
}

func TestWorkingAndCommit(t *testing.T) {
	dir := newRepo(t)

	// No commits yet: everything counts as added.
	write(t, dir, "a.txt", "a")
	files, err := Working(dir)
	if err != nil {
		t.Fatal(err)
	}
	if got := summarize(dir, files); got != "+a.txt" {
		t.Fatalf("no-HEAD: got %q", got)
	}

	write(t, dir, "b.txt", "b")
	write(t, dir, "c.txt", "this file has enough content to be detected as a rename")
	run(t, dir, "add", ".")
	run(t, dir, "commit", "-q", "-m", "one")

	// Root commit: its own files.
	files, err = Commit(dir, "HEAD")
	if err != nil {
		t.Fatal(err)
	}
	if got := summarize(dir, files); got != "+a.txt +b.txt +c.txt" {
		t.Fatalf("root commit: got %q", got)
	}

	write(t, dir, "a.txt", "changed")   // modified, unstaged
	run(t, dir, "rm", "-q", "b.txt")    // deleted, staged
	run(t, dir, "mv", "c.txt", "d.txt") // renamed, staged
	write(t, dir, "new.txt", "new")     // untracked
	files, err = Working(dir)
	if err != nil {
		t.Fatal(err)
	}
	if got := summarize(dir, files); got != "+a.txt +d.txt +new.txt -b.txt -c.txt" {
		t.Fatalf("working: got %q", got)
	}

	run(t, dir, "add", ".")
	run(t, dir, "commit", "-q", "-m", "two")
	files, err = Commit(dir, "HEAD")
	if err != nil {
		t.Fatal(err)
	}
	if got := summarize(dir, files); got != "+a.txt +d.txt +new.txt -b.txt -c.txt" {
		t.Fatalf("commit: got %q", got)
	}
	if _, err := Commit(dir, "nope"); err == nil {
		t.Fatal("expected an error for an unknown ref")
	}
	if files, _ = Working(dir); len(files) != 0 {
		t.Fatalf("clean tree should have no changes, got %v", files)
	}
}

func TestNotARepo(t *testing.T) {
	if _, err := Working(t.TempDir()); err == nil {
		t.Fatal("expected an error outside a git repository")
	}
}
