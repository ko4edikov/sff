package cli

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/ko4edikov/sff/pkg/auth"
	"github.com/ko4edikov/sff/pkg/mdapi"
	"github.com/ko4edikov/sff/pkg/project"
	"github.com/ko4edikov/sff/pkg/sfapi"
	"github.com/ko4edikov/sff/pkg/source"
)

func newDeleteCmd() *cobra.Command {
	var sourceDirs, metadata, tests []string
	var projectDir, testLevel, apiVersion string
	var checkOnly, noPrompt, dryRun bool
	var wait int
	cmd := &cobra.Command{
		Use:   "delete",
		Short: "Delete source from the org and the local project (like sf project delete source)",
		Long: "Delete components from the org with a destructive Metadata API deploy, then\n" +
			"remove their source files from the local project. Select what to delete with\n" +
			"-d (source files or directories inside the project) or -m Type:Name specifiers.\n" +
			"A single file maps to its narrowest component (a field file deletes just that\n" +
			"field); a bundle (lwc/aura), static resource or class always takes all of its\n" +
			"files with it. Local files are removed only after the org-side delete succeeds.\n" +
			"Without -r/--no-prompt the components and files are listed and you must confirm.\n" +
			"Use --check-only to validate the delete without changing the org or the project.",
		Example: `  sff delete -d force-app/main/default/classes/MyClass.cls
  sff delete -m ApexClass:MyClass -m LWC:myCmp
  sff delete -m CustomField:Account.Legacy__c -r
  sff delete -d force-app/main/default/objects/Account/fields --check-only
  sff delete -m ApexClass:MyClass --dry-run`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			if len(sourceDirs) == 0 && len(metadata) == 0 {
				return fmt.Errorf("specify what to delete with -d or -m")
			}
			level, err := resolveTestLevel(testLevel, tests)
			if err != nil {
				return err
			}
			opts := mdapi.DeployOptions{
				CheckOnly:       checkOnly,
				RollbackOnError: true,
				TestLevel:       level,
				RunTests:        tests,
			}
			return runDelete(cmd.Context(), deleteSelection{sourceDirs: sourceDirs, metadata: metadata, projectDir: projectDir},
				apiVersion, noPrompt, dryRun, time.Duration(wait)*time.Minute, opts)
		},
	}
	cmd.Flags().StringArrayVarP(&sourceDirs, "source-dir", "d", nil, "source file or directory to delete (repeatable)")
	cmd.Flags().StringArrayVarP(&metadata, "metadata", "m", nil, "metadata to delete as Type:Name or Type:Name1,Name2; a bare Type or * deletes every local component of it (case-insensitive, repeatable)")
	cmd.Flags().StringVar(&projectDir, "project-dir", "", "sfdx project to delete from (default: search up from cwd)")
	cmd.Flags().BoolVarP(&checkOnly, "check-only", "c", false, "validate the delete without changing the org or the local project")
	cmd.Flags().BoolVarP(&noPrompt, "no-prompt", "r", false, "don't ask for confirmation")
	cmd.Flags().BoolVar(&dryRun, "dry-run", false, "list what would be deleted and print the destructive manifest, without deleting")
	cmd.Flags().StringVarP(&testLevel, "test-level", "l", "", "NoTestRun|RunSpecifiedTests|RunLocalTests|RunAllTestsInOrg")
	cmd.Flags().StringArrayVar(&tests, "tests", nil, "Apex test class to run (repeatable; requires -l RunSpecifiedTests)")
	cmd.Flags().IntVarP(&wait, "wait", "w", 0, "minutes to wait for the delete to finish (0 = wait indefinitely)")
	cmd.Flags().StringVar(&apiVersion, "api-version", sfapi.DefaultAPIVersion, "Metadata API version")
	cmd.MarkFlagsMutuallyExclusive("source-dir", "metadata")
	addTargetOrgFlag(cmd)
	return cmd
}

// deleteSelection captures the mutually exclusive ways to choose what to delete.
type deleteSelection struct {
	sourceDirs []string // -d: files or directories inside the project
	metadata   []string // -m: Type:Name specifiers
	projectDir string   // where to find the sfdx project
}

func runDelete(ctx context.Context, sel deleteSelection, apiVersion string, noPrompt, dryRun bool, wait time.Duration, opts mdapi.DeployOptions) error {
	start := sel.projectDir
	if start == "" {
		start = "."
	}
	proj, err := project.Find(start)
	if err != nil {
		return err
	}
	org, err := auth.Resolve(targetOrg)
	if err != nil {
		return err
	}
	client := newMDClient(org)
	client.APIVersion = strings.TrimPrefix(apiVersion, "v")

	// A catalog failure is non-fatal: resolution falls back to built-in heuristics.
	catalog, _, _ := client.DescribeMetadataCached(ctx, false)

	var pkg *mdapi.Package
	if len(sel.metadata) > 0 {
		if pkg, err = mdapi.ParseSpecifiers(sel.metadata, client.APIVersion, mdapi.NewTypeResolver(catalog)); err != nil {
			return err
		}
	}
	plan, err := source.ResolveDelete(proj, sel.sourceDirs, pkg, client.APIVersion, catalog)
	if err != nil {
		return err
	}
	for _, w := range plan.Warnings {
		fmt.Fprintln(os.Stderr, "warning:", w)
	}

	destructive, err := plan.Package.XML()
	if err != nil {
		return err
	}
	printDeletePlan(os.Stdout, plan, proj.Root)
	if dryRun {
		fmt.Printf("\n%s", destructive)
		return nil
	}

	if !noPrompt && !opts.CheckOnly {
		if !stdinIsTerminal() {
			return fmt.Errorf("confirmation required: pass -r/--no-prompt to delete non-interactively")
		}
		if !confirmDelete(os.Stdin, os.Stdout) {
			fmt.Println("aborted; nothing was deleted")
			return &ExitError{Code: 1}
		}
	}

	empty := &mdapi.Package{Version: plan.Package.Version}
	emptyXML, err := empty.XML()
	if err != nil {
		return err
	}
	zipBytes, err := mdapi.BuildZip(map[string][]byte{
		"package.xml":            emptyXML,
		"destructiveChanges.xml": destructive,
	})
	if err != nil {
		return err
	}

	verb := "deleting"
	if opts.CheckOnly {
		verb = "validating delete of"
	}
	began := time.Now()
	if _, err := deployWithProgress(ctx, client, org, zipBytes, opts, verb, wait); err != nil {
		return err
	}

	if opts.CheckOnly {
		fmt.Printf("validated delete of %d component(s) in %s in %s\n", componentCount(plan.Package), org.Username, fmtDuration(time.Since(began)))
		return nil
	}
	fmt.Printf("deleted %d component(s) from %s in %s\n", componentCount(plan.Package), org.Username, fmtDuration(time.Since(began)))
	if err := plan.RemoveLocal(); err != nil {
		return fmt.Errorf("deleted from the org, but removing local files failed: %w", err)
	}
	fmt.Printf("removed %d local file(s)\n", len(plan.Files))
	return nil
}

// printDeletePlan lists the components and local files a delete will touch,
// with file paths shown relative to the project root.
func printDeletePlan(w io.Writer, plan *source.DeletePlan, root string) {
	fmt.Fprintln(w, "This operation will delete the following from your org:")
	for _, t := range plan.Package.Types {
		for _, m := range t.Members {
			fmt.Fprintf(w, "  %s:%s\n", t.Name, m)
		}
	}
	for _, path := range sortedKeys(plan.LabelEdits) {
		rel, err := filepath.Rel(root, path)
		if err != nil {
			rel = path
		}
		fmt.Fprintf(w, "and remove %d label(s) from %s on your computer\n", len(plan.LabelEdits[path]), rel)
	}
	if len(plan.Files) == 0 {
		return
	}
	fmt.Fprintln(w, "and the following files from your computer:")
	for _, f := range plan.Files {
		rel, err := filepath.Rel(root, f)
		if err != nil {
			rel = f
		}
		fmt.Fprintf(w, "  %s\n", rel)
	}
}

func sortedKeys(m map[string][]string) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

// confirmDelete asks for a y/yes answer on in, writing the prompt to out.
func confirmDelete(in io.Reader, out io.Writer) bool {
	fmt.Fprint(out, "\nAre you sure you want to proceed? (y/N) ")
	line, _ := bufio.NewReader(in).ReadString('\n')
	switch strings.ToLower(strings.TrimSpace(line)) {
	case "y", "yes":
		return true
	}
	return false
}

// stdinIsTerminal reports whether stdin is an interactive terminal.
func stdinIsTerminal() bool {
	fi, err := os.Stdin.Stat()
	return err == nil && fi.Mode()&os.ModeCharDevice != 0
}
