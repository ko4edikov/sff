# `sff pull` (full project refresh) Implementation Plan

> **For agentic workers:** No `superpowers:*` execution skills are available in this
> environment. Execute task-by-task either inline in this session, or by dispatching
> one fresh subagent per task via the `Agent` tool (general-purpose type) with two-stage
> review between tasks. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Add `sff pull` — a command that refreshes the whole local project from an org
(retrieve everything the project already tracks, re-placed into whatever custom folder
structure the files already lived in), leaving a clean `git diff` for review.

**Architecture:** Two building blocks in `pkg/source` (a recursive placement index and a
local-scope scanner), both reusing the existing decomposition/registry tables so they
agree with how `sff retrieve`/`sff diff` already classify files. A new `internal/cli/pull.go`
command orchestrates: verify clean git stage → snapshot placement index + scope from the
*current* tree → wipe package dirs → retrieve the snapshotted scope via the existing
`mdapi`/`source.ConvertZipToSource` pipeline → convert back into source format using the
*snapshotted* index (not the now-empty tree), so files land back exactly where they were,
even inside arbitrary custom module folders.

**Tech Stack:** Go, cobra (CLI), existing `pkg/mdapi` (Metadata API), `pkg/source`
(decompose/recompose), `pkg/project` (sfdx-project.json), `os/exec` for git status checks.

## Global Constraints

- Tab-indented Go; match existing package conventions (see `pkg/source/convert.go`,
  `pkg/source/recompose_members.go`).
- No behavior change to plain `sff retrieve` / `sff diff` — all new parameters default to
  `nil`/prior behavior when unset, verified by existing tests continuing to pass.
- `sff pull` must refuse to run against a dirty git working tree unless `--force` is passed
  (destructive-action safety: this command deletes files).
- Follow repo convention: commands that hit an org take `-o` (`addTargetOrgFlag`).

---

## File Structure

- `pkg/source/place_index.go` *(new)* — `BuildPlacementIndex`, `knownTypeDirs`,
  `relFromTypeDir`: builds the `srcRel → absolute path` index by walking a project's
  existing files, regardless of nesting depth.
- `pkg/source/convert.go` *(modify)* — `placeInProject` gains an `index` parameter;
  `ConvertZipToSource` gains an `index` parameter (nil ⇒ build fresh, current behavior).
- `pkg/source/fetch.go` *(modify)* — update the 4 `placeInProject` call sites and the 2
  `ConvertZipToSource` call sites for the new parameters (pass `nil`, no behavior change).
- `pkg/source/place_test.go` *(modify)* — update existing calls for the new parameter,
  add coverage for nested/custom module placement.
- `pkg/source/local_scope.go` *(new)* — `LocalScope(dirs []string, catalog
  *mdapi.DescribeResult) []string`: scans existing files and returns the `Type:Name`
  specifiers already tracked locally.
- `pkg/source/local_scope_test.go` *(new)* — tests for `LocalScope`.
- `internal/cli/pull.go` *(new)* — `newPullCmd()` / `runPull(...)`, registered in
  `internal/cli/root.go`.
- `internal/cli/pull_test.go` *(new)* — tests for the git-clean check and scope/placement
  wiring (using a fake org/client the way `internal/cli/deploy_test.go` does — check that
  file first for the existing fake-client pattern before writing new test doubles).
- `internal/cli/root.go` *(modify)* — `root.AddCommand(newPullCmd())`.

---

### Task 1: Recursive placement index (`pkg/source`)

**Files:**
- Create: `pkg/source/place_index.go`
- Modify: `pkg/source/convert.go`
- Modify: `pkg/source/fetch.go`
- Modify: `pkg/source/place_test.go`
- Test: `pkg/source/place_index_test.go`

**Interfaces:**
- Produces: `func BuildPlacementIndex(dirs []string, catalog *mdapi.DescribeResult) map[string]string`
  — keys are `srcRel` strings exactly as produced elsewhere in `convert.go` (e.g.
  `"classes/Foo.cls-meta.xml"`, `"objects/Account/fields/Bar__c.field-meta.xml"`); values
  are absolute file paths.
- Modifies: `placeInProject(p *project.Project, dest, srcRel string, index map[string]string) string`
  (was 3 args, now 4 — `index` may be `nil`).
- Modifies: `ConvertZipToSource(zipBytes []byte, p *project.Project, catalog *mdapi.DescribeResult, dest string, index map[string]string) (*ConvertResult, error)`
  (was 4 args, now 5 — `index` may be `nil`, meaning "build fresh from `p`'s current tree",
  which is today's behavior).

- [ ] **Step 1: Write the failing test for `BuildPlacementIndex`**

Create `pkg/source/place_index_test.go`:

```go
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
		"classes/Foo.cls-meta.xml":                       "source/authorization/src/classes/Foo.cls-meta.xml",
		"objects/Account/fields/Bar__c.field-meta.xml":    "source/authorization/src/objects/Account/fields/Bar__c.field-meta.xml",
		"lwc/calcWidget/calcWidget.js":                    "source/calculatorService/src/lwc/calcWidget/calcWidget.js",
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
```

- [ ] **Step 2: Run the test to verify it fails**

Run: `go test ./pkg/source/... -run TestBuildPlacementIndexFindsNestedModuleFiles -v`
Expected: FAIL — `undefined: BuildPlacementIndex`

- [ ] **Step 3: Implement `BuildPlacementIndex`**

Create `pkg/source/place_index.go`:

```go
package source

import (
	"io/fs"
	"path/filepath"
	"strings"

	"github.com/ko4edikov/sff/pkg/mdapi"
)

// BuildPlacementIndex walks every directory in dirs once and maps each existing
// file's type-relative suffix (the same "srcRel" strings convert.go already
// produces, e.g. "classes/Foo.cls-meta.xml" or
// "objects/Account/fields/Bar__c.field-meta.xml") to its absolute path.
//
// This lets placeInProject find a component's existing file no matter how
// deep a project nests its package directory — e.g. a custom feature-module
// layout like "authorization/src/classes/Foo.cls-meta.xml" — matching how
// `sf project retrieve start` locates local components anywhere in the tree
// instead of only directly under the package root or its main/default.
func BuildPlacementIndex(dirs []string, catalog *mdapi.DescribeResult) map[string]string {
	typeDirs := knownTypeDirs(catalog)
	index := make(map[string]string)
	for _, root := range dirs {
		filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
			if err != nil || d.IsDir() {
				return nil
			}
			rel := relFromTypeDir(root, p, typeDirs)
			if rel == "" {
				return nil
			}
			if _, exists := index[rel]; !exists {
				index[rel] = p
			}
			return nil
		})
	}
	return index
}

// knownTypeDirs is the set of source directory names that mark the start of a
// component's type-relative path (e.g. "classes", "objects"): every
// directoryName from the describe catalog when available, plus the
// decomposition table's directories and the no-catalog fallback map, so
// placement still works when describe failed or wasn't fetched.
func knownTypeDirs(catalog *mdapi.DescribeResult) map[string]bool {
	dirs := make(map[string]bool, len(dirToType)+len(decompByDir))
	for dir := range dirToType {
		dirs[dir] = true
	}
	for dir := range decompByDir {
		dirs[dir] = true
	}
	if catalog != nil {
		for _, o := range catalog.Objects {
			dirs[o.DirectoryName] = true
		}
	}
	return dirs
}

// relFromTypeDir returns p's slash-separated suffix (relative to root)
// starting at the outermost path segment that names a known metadata type
// directory, or "" if none matches. The outermost match is used so a
// component nested arbitrarily deep under custom module folders still keys
// on e.g. "classes/...", matching the srcRel format placeInProject expects.
func relFromTypeDir(root, p string, typeDirs map[string]bool) string {
	relPath, err := filepath.Rel(root, p)
	if err != nil {
		return ""
	}
	parts := strings.Split(filepath.ToSlash(relPath), "/")
	for i, part := range parts {
		if typeDirs[part] {
			return strings.Join(parts[i:], "/")
		}
	}
	return ""
}
```

- [ ] **Step 4: Run the test to verify it passes**

Run: `go test ./pkg/source/... -run TestBuildPlacementIndexFindsNestedModuleFiles -v`
Expected: PASS

- [ ] **Step 5: Commit**

```bash
git add pkg/source/place_index.go pkg/source/place_index_test.go
git commit -m "feat: add recursive placement index for nested project layouts"
```

- [ ] **Step 6: Write the failing test for `placeInProject` using the index**

Add to `pkg/source/place_test.go` (existing file — update its two existing calls to pass
`nil` as the new 4th arg, then add this new test):

```go
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
```

Also update the file's existing two calls:

```go
got := placeInProject(proj, filepath.Join("out", "sub"), rel, nil)
```

```go
got := placeInProject(proj, "", rel, nil)
```

Add the `"github.com/ko4edikov/sff/pkg/project"` import to `place_test.go` if not already
present (check the file — it currently imports only `path/filepath` and `testing`).

- [ ] **Step 7: Run to verify it fails**

Run: `go test ./pkg/source/... -run TestPlaceInProjectUsesIndexForNestedModules -v`
Expected: FAIL — compile error (`placeInProject` still takes 3 args)

- [ ] **Step 8: Update `placeInProject` and its 7 call sites**

In `pkg/source/convert.go`, change the signature and body:

```go
// placeInProject returns the path to write srcRel to. When dest is non-empty it
// is an explicit output root and the file goes to dest/<srcRel> verbatim
// (nothing is merged into the project). Otherwise placement checks index first
// (an existing file anywhere in the tree, including nested custom module
// folders — see BuildPlacementIndex), then the standard two candidate
// locations, else a new file lands under the default package directory's
// main/default tree.
func placeInProject(p *project.Project, dest, srcRel string, index map[string]string) string {
	rel := filepath.FromSlash(srcRel)
	if dest != "" {
		return filepath.Join(dest, rel)
	}
	if index != nil {
		if hit, ok := index[srcRel]; ok {
			return hit
		}
	}
	for _, d := range p.AbsDirs() {
		for _, cand := range []string{
			filepath.Join(d, "main", "default", rel),
			filepath.Join(d, rel),
		} {
			if _, err := os.Stat(cand); err == nil {
				return cand
			}
		}
	}
	base := p.DefaultDir()
	if fi, err := os.Stat(filepath.Join(base, "main", "default")); err == nil && fi.IsDir() {
		return filepath.Join(base, "main", "default", rel)
	}
	return filepath.Join(base, rel)
}
```

Update `ConvertZipToSource` in the same file to build (or accept) the index once, and pass
it to every `placeInProject` call:

```go
func ConvertZipToSource(zipBytes []byte, p *project.Project, catalog *mdapi.DescribeResult, dest string, index map[string]string) (*ConvertResult, error) {
	zr, err := zip.NewReader(bytes.NewReader(zipBytes), int64(len(zipBytes)))
	if err != nil {
		return nil, fmt.Errorf("open retrieved zip: %w", err)
	}
	if dest == "" && index == nil {
		index = BuildPlacementIndex(p.AbsDirs(), catalog)
	}

	byDir := catalogByDir(catalog)
	srContentTypes := staticResourceContentTypes(zr)
	res := &ConvertResult{}
	for _, f := range zr.File {
		...
```

(Body unchanged except every `placeInProject(p, dest, X)` call becomes
`placeInProject(p, dest, X, index)` — 4 call sites at what are currently lines 74, 86, 102,
117.)

Update `pkg/source/fetch.go`'s 4 `placeInProject` call sites and 2 `ConvertZipToSource`
calls to pass `nil` for the new parameter (temp-dir conversions and single-file diff
lookups don't need the index — they're not bulk operations and `tmpProj` is always freshly
emptied, so the fast-path fallback in `placeInProject` already does the right thing):

```go
conv, err := ConvertZipToSource(res.ZipFile, tmpProj, catalog, "", nil)
```

```go
remote := placeInProject(tmpProj, "", rel, nil)
```
```go
LocalPath:  placeInProject(t.Project, "", rel, nil),
```
```go
if fi, err := os.Stat(placeInProject(t.Project, "", t.ScopeRel, nil)); err != nil || fi.IsDir() {
```
```go
LocalPath:  placeInProject(t.Project, "", rel, nil),
```

Update the doc comment above `ConvertZipToSource` to mention the new parameter:

```go
// ... dest, when non-empty, overrides placement: every converted source file is
// written verbatim under dest/<source-rel> (see placeInProject) instead of
// being merged into the project's package directories.
// index, when non-nil, is used instead of scanning p's current tree (see
// BuildPlacementIndex) — pass a snapshot taken before the tree was modified
// (e.g. before a full-project wipe) so files land back where they were. Pass
// nil to build the index fresh from p's current state (the normal case).
```

Finally update `internal/cli/retrieve.go`'s single call site:

```go
conv, err := source.ConvertZipToSource(res.ZipFile, proj, catalog, sourceDest, nil)
```

- [ ] **Step 9: Run the full `pkg/source` and `internal/cli` test suites**

Run: `go build ./... && go test ./pkg/source/... ./internal/cli/... -v`
Expected: PASS (including `TestPlaceInProjectUsesIndexForNestedModules`,
`TestPlaceInProjectDestOverride`, `TestPlaceInProjectDefaultDir`, and every
pre-existing `pkg/source`/`internal/cli` test unaffected)

- [ ] **Step 10: Commit**

```bash
git add pkg/source/convert.go pkg/source/fetch.go pkg/source/place_test.go internal/cli/retrieve.go
git commit -m "feat: thread placement index through convert so nested module layouts merge correctly"
```

---

### Task 2: Local scope scanner (`pkg/source`)

**Files:**
- Create: `pkg/source/local_scope.go`
- Test: `pkg/source/local_scope_test.go`

**Interfaces:**
- Consumes: `decompByDir`, `dirToType`, `bundleDirs`, `catalogByDir`, `isVerbatim` (all
  already defined in `pkg/source`, from Task 1's exploration — no changes needed to them).
- Produces: `func LocalScope(dirs []string, catalog *mdapi.DescribeResult) []string` —
  returns deduplicated `Type:Name` specifiers (or bare `Type` for whole-file types like
  `CustomLabels`) matching the `-m` flag syntax `mdapi.ParseSpecifiers`/`BuildPackage`
  already accept (see `internal/cli/retrieve.go`'s use of `mdapi.BuildPackage`).

- [ ] **Step 1: Write the failing test**

Create `pkg/source/local_scope_test.go`:

```go
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
	writeFile(root+"/source/authorization/src/labels/CustomLabels.labels-meta.xml", []byte("<x/>"))

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
```

- [ ] **Step 2: Run to verify it fails**

Run: `go test ./pkg/source/... -run TestLocalScope -v`
Expected: FAIL — `undefined: LocalScope`

- [ ] **Step 3: Implement `LocalScope`**

Create `pkg/source/local_scope.go`:

```go
package source

import (
	"io/fs"
	"path/filepath"
	"strings"

	"github.com/ko4edikov/sff/pkg/mdapi"
)

// LocalScope scans dirs (a project's package directories) and returns the
// Metadata API Type:Name specifiers already tracked locally — e.g.
// "ApexClass:Foo", "CustomObject:Account", or the bare type name
// "CustomLabels" for a topLevel (single shared file) type. Specifiers are
// deduplicated and match the syntax mdapi.BuildPackage/ParseSpecifiers accept,
// so the result can be fed straight into a retrieve.
//
// Classification reuses the same tables convert.go and place_index.go use
// (decompByDir, dirToType, the describe catalog), so a project's scope is
// always computed the same way its files are placed, regardless of how deep a
// custom module layout nests them.
func LocalScope(dirs []string, catalog *mdapi.DescribeResult) []string {
	byDir := catalogByDir(catalog)
	seen := make(map[string]bool)
	var specs []string
	add := func(spec string) {
		if !seen[spec] {
			seen[spec] = true
			specs = append(specs, spec)
		}
	}

	for _, root := range dirs {
		filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
			if err != nil || d.IsDir() {
				return nil
			}
			rel, err := filepath.Rel(root, p)
			if err != nil {
				return nil
			}
			parts := strings.Split(filepath.ToSlash(rel), "/")
			if len(parts) >= 2 && parts[0] == "main" && parts[1] == "default" {
				parts = parts[2:]
			}
			if len(parts) == 0 {
				return nil
			}
			spec, ok := classifyLocalFile(parts, byDir)
			if ok {
				add(spec)
			}
			return nil
		})
	}
	return specs
}

// classifyLocalFile turns a type-relative path (parts[0] is the type
// directory, e.g. "classes") into a Type:Name specifier.
func classifyLocalFile(parts []string, byDir map[string]mdapi.MetadataObject) (string, bool) {
	typeDir := parts[0]

	if t := decompByDir[typeDir]; t != nil {
		if t.Layout == "topLevel" {
			return t.Name, true
		}
		if len(parts) < 2 {
			return "", false
		}
		return t.Name + ":" + parts[1], true
	}

	typeName := dirNameToType(typeDir, byDir)
	if typeName == "" {
		return "", false
	}
	if len(parts) < 2 {
		return "", false
	}
	if isVerbatim(typeDir, byDir) && (bundleDirs[typeDir] || (byDir != nil && byDir[typeDir].Suffix == "")) {
		return typeName + ":" + parts[1], true // bundle: dir/Name/...
	}
	if len(parts) != 2 {
		return "", false // a bundle member file under an unrecognized bundle dir
	}
	return typeName + ":" + memberStem(parts[1]), true
}

// dirNameToType resolves a source directory name back to its Metadata API
// type name, preferring the describe catalog and falling back to the static
// no-catalog map (dirToType, defined in recompose.go).
func dirNameToType(dir string, byDir map[string]mdapi.MetadataObject) string {
	if byDir != nil {
		if o, ok := byDir[dir]; ok {
			return o.Name
		}
	}
	return dirToType[dir]
}

// memberStem strips a trailing "-meta.xml" and the file's extension, leaving
// the component's Metadata API name (e.g. "Foo.cls-meta.xml" → "Foo").
func memberStem(name string) string {
	name = strings.TrimSuffix(name, "-meta.xml")
	if i := strings.LastIndex(name, "."); i >= 0 {
		name = name[:i]
	}
	return name
}
```

- [ ] **Step 4: Run to verify it passes**

Run: `go test ./pkg/source/... -run TestLocalScope -v`
Expected: PASS

- [ ] **Step 5: Run the full package test suite (regression check)**

Run: `go build ./... && go test ./... -v`
Expected: PASS — no other package references `dirNameToType`/`memberStem`, so no
collisions; confirm there isn't already a same-named helper elsewhere in `pkg/source`
before committing (`grep -rn "func memberStem\|func dirNameToType" pkg/source`).

- [ ] **Step 6: Commit**

```bash
git add pkg/source/local_scope.go pkg/source/local_scope_test.go
git commit -m "feat: add LocalScope to derive a project's tracked metadata from its files"
```

---

### Task 3: `sff pull` command (`internal/cli`)

**Files:**
- Create: `internal/cli/pull.go`
- Modify: `internal/cli/root.go`
- Test: `internal/cli/pull_test.go`

**Interfaces:**
- Consumes: `source.BuildPlacementIndex`, `source.LocalScope`, `source.ConvertZipToSource`
  (Tasks 1–2); `project.Find`, `project.Project.AbsDirs` (`pkg/project`); `auth.Resolve`,
  `newMDClient`, `addTargetOrgFlag` (existing, `internal/cli/root.go`/`org.go`);
  `mdapi.BuildPackage`, `mdapi.NewTypeResolver`, `client.RetrieveAndWait`,
  `client.DescribeMetadataCached` (existing, used the same way as `runRetrieve` in
  `internal/cli/retrieve.go`); `progress.Start` (`pkg/progress`).
- Produces: `newPullCmd() *cobra.Command`, registered in `root.go`.

- [ ] **Step 1: Read `internal/cli/deploy_test.go` for the existing fake-org/client test pattern**

Before writing `pull_test.go`, open `internal/cli/deploy_test.go` (and `diff_ignore_test.go`
if it also stubs a client) to find how existing CLI tests avoid hitting a real org — reuse
that pattern rather than inventing a new one. Note the fake type's name and how it's wired
into `runDeploy`/`runRetrieve`-equivalent functions so `runPull` can be tested the same way.

- [ ] **Step 2: Write the failing test for the git-clean guard**

Create `internal/cli/pull_test.go` (adjust the fake-client wiring per what Step 1 found;
this step's test only exercises the git-check, which needs no org access):

```go
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
```

- [ ] **Step 3: Run to verify it fails**

Run: `go test ./internal/cli/... -run TestCheckGitClean -v`
Expected: FAIL — `undefined: checkGitClean`

- [ ] **Step 4: Implement `internal/cli/pull.go`**

```go
package cli

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/ko4edikov/sff/pkg/auth"
	"github.com/ko4edikov/sff/pkg/mdapi"
	"github.com/ko4edikov/sff/pkg/progress"
	"github.com/ko4edikov/sff/pkg/project"
	"github.com/ko4edikov/sff/pkg/sfapi"
	"github.com/ko4edikov/sff/pkg/source"
)

func newPullCmd() *cobra.Command {
	var projectDir, apiVersion string
	var force bool
	cmd := &cobra.Command{
		Use:   "pull",
		Short: "Refresh the whole local project from an org",
		Long: "Refresh every metadata component the project already tracks: snapshot which\n" +
			"types/members and folder each local file lives in, wipe the package directories,\n" +
			"retrieve that same scope from the org, and write it back into source format at\n" +
			"the same paths (including custom, non-standard folder layouts). Review the result\n" +
			"with `git status`/`git diff` afterward.\n\n" +
			"Refuses to run against a dirty git working tree (uncommitted changes would be\n" +
			"indistinguishable from the refresh) unless --force is passed.",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			return runPull(cmd.Context(), projectDir, apiVersion, force)
		},
	}
	cmd.Flags().StringVar(&projectDir, "project-dir", "", "sfdx project to refresh (default: search up from cwd)")
	cmd.Flags().StringVar(&apiVersion, "api-version", sfapi.DefaultAPIVersion, "Metadata API version")
	cmd.Flags().BoolVar(&force, "force", false, "proceed even if the git working tree has uncommitted changes")
	addTargetOrgFlag(cmd)
	return cmd
}

func runPull(ctx context.Context, projectDir, apiVersion string, force bool) error {
	start := projectDir
	if start == "" {
		start = "."
	}
	proj, err := project.Find(start)
	if err != nil {
		return err
	}

	if err := checkGitClean(proj.Root, force); err != nil {
		return err
	}

	org, err := auth.Resolve(targetOrg)
	if err != nil {
		return err
	}
	client := newMDClient(org)
	client.APIVersion = strings.TrimPrefix(apiVersion, "v")
	catalog, _, _ := client.DescribeMetadataCached(ctx, false)

	dirs := proj.AbsDirs()
	index := source.BuildPlacementIndex(dirs, catalog)
	specs := source.LocalScope(dirs, catalog)
	if len(specs) == 0 {
		return fmt.Errorf("no local metadata found under %v; nothing to pull", dirs)
	}

	for _, d := range dirs {
		if err := os.RemoveAll(d); err != nil {
			return fmt.Errorf("clear %s: %w", d, err)
		}
		if err := os.MkdirAll(d, 0o755); err != nil {
			return err
		}
	}

	pkg, err := mdapi.BuildPackage("", specs, apiVersion, mdapi.NewTypeResolver(catalog))
	if err != nil {
		return err
	}

	pullStart := time.Now()
	prog := progress.Start("pulling")
	res, err := client.RetrieveAndWait(ctx, pkg, func(attempt int) {
		prog.Update(fmt.Sprintf("pulling (poll %d)", attempt))
	})
	prog.Stop()
	if err != nil {
		return err
	}
	for _, m := range res.Messages {
		fmt.Fprintln(os.Stderr, "warning:", m)
	}

	conv, err := source.ConvertZipToSource(res.ZipFile, proj, catalog, "", index)
	if err != nil {
		return err
	}
	for _, w := range conv.Warnings {
		fmt.Fprintln(os.Stderr, "warning:", w)
	}

	fmt.Printf("pulled %d component(s), %d file(s) in %s\n", len(specs), len(conv.Written), fmtDuration(time.Since(pullStart)))
	fmt.Println("review with: git status / git diff")
	return nil
}

// checkGitClean fails when root's git working tree has uncommitted changes
// (staged, unstaged, or untracked), unless force is set. This guards `sff
// pull`'s wipe-then-retrieve step: without a clean baseline, the resulting
// git diff would mix the org refresh with the user's own in-progress work.
func checkGitClean(root string, force bool) error {
	if force {
		return nil
	}
	cmd := exec.Command("git", "-C", root, "status", "--porcelain")
	out, err := cmd.Output()
	if err != nil {
		return fmt.Errorf("git status: %w", err)
	}
	if strings.TrimSpace(string(out)) != "" {
		return fmt.Errorf("working tree at %s is not clean:\n%s\ncommit or stash your changes first, or pass --force to proceed anyway", root, out)
	}
	return nil
}

var _ = filepath.Join // silence unused import if trimmed during review
```

(Drop the trailing `var _ = filepath.Join` line if `filepath` ends up used elsewhere in the
file — it isn't needed once the file is final; included here only because the plan's code
block must compile standalone. An implementer should remove it and the now-unused
`path/filepath` import.)

- [ ] **Step 5: Run to verify the git-clean tests pass**

Run: `go build ./... && go test ./internal/cli/... -run TestCheckGitClean -v`
Expected: PASS

- [ ] **Step 6: Register the command**

In `internal/cli/root.go`, add after `root.AddCommand(newDiffCmd())`:

```go
	root.AddCommand(newPullCmd())
```

- [ ] **Step 7: Run to verify it builds and the full CLI test suite passes**

Run: `go build ./... && go vet ./... && go test ./... -v`
Expected: PASS

- [ ] **Step 8: Manual smoke test against a real (non-production) org**

Run inside a scratch/dev org project directory, on a clean git branch:

```bash
./sff pull -o <dev-org-alias>
git status
git diff --stat
```

Confirm: files that existed before the pull land back in the exact same (possibly nested,
custom) directories; new org-side components land under the default package dir; the diff
shows no LF/CRLF or empty-tag noise (this already comes from `normalizeXML` in
`convert.go`, unchanged by this plan). This step is not automatable in CI — do it by hand
and report the result before merging.

- [ ] **Step 9: Commit**

```bash
git add internal/cli/pull.go internal/cli/pull_test.go internal/cli/root.go
git commit -m "feat: add sff pull to refresh the whole project from an org"
```

---

## Self-Review Notes

- **Spec coverage:** placement into nested/custom module folders → Task 1; "only retrieve
  types already in the tree" → Task 2 (`LocalScope`); clean-stage check + warn/abort +
  wipe + retrieve + report-for-review workflow the user described → Task 3. LF/CRLF and
  empty-tag noise were already solved by the pre-existing `normalizeXML` in `convert.go`
  (confirmed while reading the file) — no new task needed for that part.
- **Not covered (deliberately out of scope):** deciding where genuinely new org-side
  components should land when they don't match any existing local module — they fall back
  to `DefaultDir()`/`main/default`, same as plain `sff retrieve` today. Extending `LocalScope`
  to a `--only <types>` filter, and a `--dry-run` flag for `sff pull`, are natural
  follow-ups but weren't asked for.
- **Type/signature consistency:** `placeInProject`, `ConvertZipToSource` and their call
  sites in `convert.go`, `fetch.go`, `internal/cli/retrieve.go`, and the new `pull.go` all
  use the same 4-/5-argument shapes throughout.
