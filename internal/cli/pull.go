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
	var projectDir, apiVersion, ignore string
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
			"indistinguishable from the refresh) unless --force is passed.\n\n" +
			"--ignore reverts any retrieved file back to its committed version when the only\n" +
			"difference is whitespace/blank lines (same semantics as `sff diff --ignore`), so\n" +
			"those files don't show up in `git status` at all.",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			mode, err := parseIgnore(ignore)
			if err != nil {
				return err
			}
			return runPull(cmd.Context(), projectDir, apiVersion, force, mode)
		},
	}
	cmd.Flags().StringVar(&projectDir, "project-dir", "", "sfdx project to refresh (default: search up from cwd)")
	cmd.Flags().StringVar(&apiVersion, "api-version", sfapi.DefaultAPIVersion, "Metadata API version")
	cmd.Flags().BoolVar(&force, "force", false, "proceed even if the git working tree has uncommitted changes")
	cmd.Flags().StringVar(&ignore, "ignore", "none", "revert whitespace-only diffs after pulling: none, trim, whitespace, whitespace-blank")
	addTargetOrgFlag(cmd)
	return cmd
}

func runPull(ctx context.Context, projectDir, apiVersion string, force bool, ignore ignoreMode) error {
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
	fmt.Printf("scanned local scope: %d metadata component(s)\n", len(specs))

	for _, d := range dirs {
		fmt.Printf("cleaning directory %s\n", d)
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
	fmt.Printf("retrieving metadata types: %s\n", strings.Join(packageTypeNames(pkg), ", "))

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

	fmt.Println("converting retrieved metadata to source format")
	conv, err := source.ConvertZipToSource(res.ZipFile, proj, catalog, "", index)
	if err != nil {
		return err
	}
	for _, w := range conv.Warnings {
		fmt.Fprintln(os.Stderr, "warning:", w)
	}

	if ignore != ignoreNone {
		fmt.Println("reverting whitespace-only diffs")
		reverted, err := revertIgnorableDiffs(proj.Root, dirs, ignore)
		if err != nil {
			return err
		}
		fmt.Printf("reverted %d whitespace-only file(s)\n", reverted)
	}

	fmt.Printf("pulled %d component(s), %d file(s) in %s\n", len(specs), len(conv.Written), fmtDuration(time.Since(pullStart)))
	fmt.Println("review with: git status / git diff")
	return nil
}

// revertIgnorableDiffs finds every file changed under dirs (relative to git
// root) whose only difference from HEAD is whitespace/blank lines per mode
// (the same comparison `sff diff --ignore` uses), and checks it back out to
// its committed content so it drops out of `git status` entirely. Newly added
// files (absent from HEAD) are never reverted — an empty-vs-content diff is
// never whitespace-only. Returns the number of files reverted.
func revertIgnorableDiffs(root string, dirs []string, mode ignoreMode) (int, error) {
	args := append([]string{"-C", root, "diff", "--name-only", "--"}, dirs...)
	out, err := exec.Command("git", args...).Output()
	if err != nil {
		return 0, fmt.Errorf("git diff --name-only: %w", err)
	}
	var reverted []string
	for _, rel := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		if rel == "" {
			continue
		}
		head, err := exec.Command("git", "-C", root, "show", "HEAD:"+rel).Output()
		if err != nil {
			continue // new file (not in HEAD): a real addition, never whitespace-only
		}
		work, err := os.ReadFile(filepath.Join(root, rel))
		if err != nil {
			continue
		}
		aOrig, aKey := prepLines(toLines(head), mode)
		bOrig, bKey := prepLines(toLines(work), mode)
		if unifiedDiff(aOrig, bOrig, aKey, bKey, "head", "", "work", "", 0) != "" {
			continue // a real change under this ignore mode
		}
		reverted = append(reverted, rel)
	}
	if len(reverted) == 0 {
		return 0, nil
	}
	args = append([]string{"-C", root, "checkout", "--"}, reverted...)
	if err := exec.Command("git", args...).Run(); err != nil {
		return 0, fmt.Errorf("git checkout: %w", err)
	}
	return len(reverted), nil
}

// packageTypeNames returns pkg's metadata type names, in manifest order, for
// a human-readable progress line (e.g. "retrieving metadata types: ApexClass,
// CustomObject, LightningComponentBundle").
func packageTypeNames(pkg *mdapi.Package) []string {
	names := make([]string, 0, len(pkg.Types))
	for _, t := range pkg.Types {
		names = append(names, t.Name)
	}
	return names
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
