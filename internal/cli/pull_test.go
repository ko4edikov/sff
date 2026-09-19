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

// gitCommitAll stages and commits every file under dir.
func gitCommitAll(t *testing.T, dir, msg string) {
	t.Helper()
	run := func(args ...string) {
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	run("add", "-A")
	run("commit", "-q", "-m", msg)
}

func TestRevertIgnorableDiffsRevertsWhitespaceOnly(t *testing.T) {
	dir := t.TempDir()
	initGitRepo(t, dir)
	pkg := filepath.Join(dir, "force-app")
	if err := os.MkdirAll(filepath.Join(pkg, "classes"), 0o755); err != nil {
		t.Fatal(err)
	}
	whitespaceFile := filepath.Join(pkg, "classes", "Whitespace.cls")
	realFile := filepath.Join(pkg, "classes", "Real.cls")
	if err := os.WriteFile(whitespaceFile, []byte("public class Whitespace {\n}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(realFile, []byte("public class Real {\n}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	gitCommitAll(t, dir, "initial")

	// One file changes only by trailing whitespace/blank lines; the other gets
	// a real content change.
	if err := os.WriteFile(whitespaceFile, []byte("public class Whitespace {  \n\n}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(realFile, []byte("public class Real {\n\tInteger x = 1;\n}\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	reverted, err := revertIgnorableDiffs(dir, []string{pkg}, ignoreWSBlank)
	if err != nil {
		t.Fatal(err)
	}
	if reverted != 1 {
		t.Fatalf("reverted = %d, want 1", reverted)
	}

	got, err := os.ReadFile(whitespaceFile)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "public class Whitespace {\n}\n" {
		t.Fatalf("Whitespace.cls was not reverted: %q", got)
	}

	got, err = os.ReadFile(realFile)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "public class Real {\n\tInteger x = 1;\n}\n" {
		t.Fatalf("Real.cls should not have been reverted: %q", got)
	}
}

// TestRevertIgnorableDiffsHandlesReflowedLines reproduces a real case: an
// Apex Component retrieve merged two source lines into one (Salesforce
// reflowed "<apex:component ...>\n{!X}" into "<apex:component ...>{!X}"). A
// strictly line-by-line whitespace comparison sees a real change (the line
// count differs); whitespace-blank must still treat it as whitespace-only
// since every non-whitespace character is identical.
func TestRevertIgnorableDiffsHandlesReflowedLines(t *testing.T) {
	dir := t.TempDir()
	initGitRepo(t, dir)
	pkg := filepath.Join(dir, "force-app")
	if err := os.MkdirAll(filepath.Join(pkg, "components"), 0o755); err != nil {
		t.Fatal(err)
	}
	compFile := filepath.Join(pkg, "components", "EUR_Datetime.component")
	committed := "<apex:component access=\"global\">\n    {!FormattedDatetime}\n</apex:component>"
	if err := os.WriteFile(compFile, []byte(committed), 0o644); err != nil {
		t.Fatal(err)
	}
	gitCommitAll(t, dir, "initial")

	reflowed := "<apex:component access=\"global\">{!FormattedDatetime}\n</apex:component>"
	if err := os.WriteFile(compFile, []byte(reflowed), 0o644); err != nil {
		t.Fatal(err)
	}

	reverted, err := revertIgnorableDiffs(dir, []string{pkg}, ignoreWSBlank)
	if err != nil {
		t.Fatal(err)
	}
	if reverted != 1 {
		t.Fatalf("reverted = %d, want 1", reverted)
	}

	got, err := os.ReadFile(compFile)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != committed {
		t.Fatalf("EUR_Datetime.component was not reverted: %q", got)
	}
}
