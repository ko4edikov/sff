package cli

import (
	"context"
	"fmt"
	"os"
	"sort"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/ko4edikov/sff/internal/auth"
	"github.com/ko4edikov/sff/internal/mdapi"
	"github.com/ko4edikov/sff/internal/sfapi"
	"github.com/ko4edikov/sff/internal/source"
)

// testLevels maps the accepted (case-insensitive) --test-level values to their
// canonical Metadata API names.
var testLevels = map[string]string{
	"notestrun":         mdapi.TestLevelNone,
	"runspecifiedtests": mdapi.TestLevelSpecified,
	"runlocaltests":     mdapi.TestLevelLocal,
	"runalltestsinorg":  mdapi.TestLevelAll,
}

func newDeployCmd() *cobra.Command {
	var sourceDir, testLevel, apiVersion string
	var tests []string
	var checkOnly, dryRun, ignoreWarnings bool
	cmd := &cobra.Command{
		Use:   "deploy",
		Short: "Deploy source-format metadata to an org (Metadata API)",
		Long: "Recompose a source-format directory into a metadata-format package and\n" +
			"deploy it via the Metadata API (the reverse of sff retrieve). Decomposed\n" +
			"types (objects, …) are re-composed and static resources are re-archived; a\n" +
			"package.xml manifest is built from the files found. Use --check-only to\n" +
			"validate without saving, or --dry-run to build the package without deploying.",
		Example: `  sff deploy -d force-app/main/default
  sff deploy -d force-app --check-only
  sff deploy -d force-app -l RunSpecifiedTests --tests MyTest --tests OtherTest
  sff deploy -d force-app --dry-run`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			if sourceDir == "" {
				return fmt.Errorf("specify a source directory with -d")
			}
			level, err := resolveTestLevel(testLevel, tests)
			if err != nil {
				return err
			}
			opts := mdapi.DeployOptions{
				CheckOnly:       checkOnly,
				RollbackOnError: true,
				IgnoreWarnings:  ignoreWarnings,
				TestLevel:       level,
				RunTests:        tests,
			}
			return runDeploy(cmd.Context(), sourceDir, apiVersion, dryRun, opts)
		},
	}
	cmd.Flags().StringVarP(&sourceDir, "source-dir", "d", "", "source-format directory to deploy")
	cmd.Flags().BoolVarP(&checkOnly, "check-only", "c", false, "validate the deploy without saving changes")
	cmd.Flags().StringVarP(&testLevel, "test-level", "l", "", "NoTestRun|RunSpecifiedTests|RunLocalTests|RunAllTestsInOrg")
	cmd.Flags().StringArrayVar(&tests, "tests", nil, "Apex test class to run (repeatable; requires -l RunSpecifiedTests)")
	cmd.Flags().BoolVar(&dryRun, "dry-run", false, "build the package and print the manifest without deploying")
	cmd.Flags().BoolVar(&ignoreWarnings, "ignore-warnings", false, "succeed even if the deploy reports warnings")
	cmd.Flags().StringVar(&apiVersion, "api-version", sfapi.DefaultAPIVersion, "Metadata API version")
	addTargetOrgFlag(cmd)
	return cmd
}

// resolveTestLevel canonicalizes the --test-level value and validates --tests usage.
func resolveTestLevel(level string, tests []string) (string, error) {
	canon := ""
	if level != "" {
		var ok bool
		canon, ok = testLevels[strings.ToLower(strings.TrimSpace(level))]
		if !ok {
			return "", fmt.Errorf("invalid --test-level %q (want NoTestRun, RunSpecifiedTests, RunLocalTests, or RunAllTestsInOrg)", level)
		}
	}
	if len(tests) > 0 && canon != mdapi.TestLevelSpecified {
		return "", fmt.Errorf("--tests requires --test-level RunSpecifiedTests")
	}
	if canon == mdapi.TestLevelSpecified && len(tests) == 0 {
		return "", fmt.Errorf("--test-level RunSpecifiedTests requires at least one --tests")
	}
	return canon, nil
}

func runDeploy(ctx context.Context, sourceDir, apiVersion string, dryRun bool, opts mdapi.DeployOptions) error {
	org, err := auth.Resolve(targetOrg)
	if err != nil {
		return err
	}
	client := mdapi.New(org)
	client.APIVersion = strings.TrimPrefix(apiVersion, "v")

	// The describe catalog drives type/verbatim classification; a failure here is
	// non-fatal — recompose falls back to its built-in heuristics.
	catalog, _, _ := client.DescribeMetadataCached(ctx, false)

	rec, err := source.RecomposeDir(sourceDir, client.APIVersion, catalog)
	if err != nil {
		return err
	}
	for _, w := range rec.Warnings {
		fmt.Fprintln(os.Stderr, "warning:", w)
	}

	pkgXML, err := rec.Package.XML()
	if err != nil {
		return err
	}

	if dryRun {
		fmt.Printf("would deploy %d component(s) in %d file(s):\n\n", componentCount(rec.Package), len(rec.Entries))
		fmt.Print(string(pkgXML))
		return nil
	}

	entries := rec.Entries
	entries["package.xml"] = pkgXML
	zipBytes, err := mdapi.BuildZip(entries)
	if err != nil {
		return err
	}

	verb := "deploying"
	if opts.CheckOnly {
		verb = "validating"
	}
	start := time.Now()
	res, err := client.DeployAndWait(ctx, zipBytes, opts, func(attempt int, r *mdapi.DeployResult) {
		fmt.Fprintf(os.Stderr, "\r%s… %s (%d/%d components)",
			verb, r.Status, r.NumberComponentsDeployed, r.NumberComponentsTotal)
	})
	fmt.Fprintln(os.Stderr)
	if err != nil {
		printDeployFailures(res)
		return &ExitError{Code: 1}
	}

	noun := "deployed"
	if opts.CheckOnly {
		noun = "validated"
	}
	fmt.Printf("%s %d component(s) to %s in %s\n", noun, res.NumberComponentsDeployed, org.Username, fmtDuration(time.Since(start)))
	if res.NumberTestsCompleted > 0 {
		fmt.Printf("ran %d test(s), %d failed\n", res.NumberTestsCompleted, res.NumberTestErrors)
	}
	return nil
}

// printDeployFailures writes component and test failures to stderr.
func printDeployFailures(res *mdapi.DeployResult) {
	if res == nil {
		return
	}
	if res.ErrorMessage != "" {
		fmt.Fprintln(os.Stderr, "error:", res.ErrorMessage)
	}
	failures := append([]mdapi.ComponentMessage(nil), res.ComponentFailures...)
	sort.Slice(failures, func(i, j int) bool { return failures[i].FullName < failures[j].FullName })
	for _, f := range failures {
		loc := f.FullName
		if f.ComponentType != "" {
			loc = f.ComponentType + ":" + f.FullName
		}
		if f.LineNumber > 0 {
			loc = fmt.Sprintf("%s (line %d)", loc, f.LineNumber)
		}
		fmt.Fprintf(os.Stderr, "  %s — %s\n", loc, f.Problem)
	}
	for _, t := range res.TestFailures {
		fmt.Fprintf(os.Stderr, "  test %s.%s — %s\n", t.Name, t.MethodName, t.Message)
	}
}

// componentCount totals the members across a manifest's types.
func componentCount(pkg *mdapi.Package) int {
	n := 0
	for _, t := range pkg.Types {
		n += len(t.Members)
	}
	return n
}
