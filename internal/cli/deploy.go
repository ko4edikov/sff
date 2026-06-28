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
	"github.com/ko4edikov/sff/internal/project"
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
	var sourceDir, manifest, projectDir, testLevel, apiVersion string
	var metadata, tests []string
	var checkOnly, dryRun, ignoreWarnings bool
	cmd := &cobra.Command{
		Use:   "deploy",
		Short: "Deploy source-format metadata to an org (Metadata API)",
		Long: "Recompose source-format metadata into a metadata-format package and deploy\n" +
			"it via the Metadata API (the reverse of sff retrieve). Select what to deploy\n" +
			"with -d (a whole source directory), -m Type:Name specifiers, or -x package.xml;\n" +
			"-m/-x resolve members from the sfdx project. Decomposed types (objects, …) are\n" +
			"re-composed and static resources are re-archived. Use --check-only to validate\n" +
			"without saving, or --dry-run to build the package without deploying.",
		Example: `  sff deploy -d force-app/main/default
  sff deploy -m ApexClass:MyClass -m LWC:myCmp
  sff deploy -x manifest/package.xml --check-only
  sff deploy -d force-app -l RunSpecifiedTests --tests MyTest --tests OtherTest
  sff deploy -m ApexClass --dry-run`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			if sourceDir == "" && len(metadata) == 0 && manifest == "" {
				return fmt.Errorf("specify what to deploy with -d, -m, or -x")
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
			sel := deploySelection{sourceDir: sourceDir, metadata: metadata, manifest: manifest, projectDir: projectDir}
			return runDeploy(cmd.Context(), sel, apiVersion, dryRun, opts)
		},
	}
	cmd.Flags().StringVarP(&sourceDir, "source-dir", "d", "", "source-format directory to deploy")
	cmd.Flags().StringArrayVarP(&metadata, "metadata", "m", nil, "metadata to deploy as Type or Type:Name (repeatable)")
	cmd.Flags().StringVarP(&manifest, "manifest", "x", "", "path to a package.xml listing what to deploy")
	cmd.Flags().StringVar(&projectDir, "project-dir", "", "sfdx project to resolve -m/-x members from (default: search up from cwd)")
	cmd.Flags().BoolVarP(&checkOnly, "check-only", "c", false, "validate the deploy without saving changes")
	cmd.Flags().StringVarP(&testLevel, "test-level", "l", "", "NoTestRun|RunSpecifiedTests|RunLocalTests|RunAllTestsInOrg")
	cmd.Flags().StringArrayVar(&tests, "tests", nil, "Apex test class to run (repeatable; requires -l RunSpecifiedTests)")
	cmd.Flags().BoolVar(&dryRun, "dry-run", false, "build the package and print the manifest without deploying")
	cmd.Flags().BoolVar(&ignoreWarnings, "ignore-warnings", false, "succeed even if the deploy reports warnings")
	cmd.Flags().StringVar(&apiVersion, "api-version", sfapi.DefaultAPIVersion, "Metadata API version")
	cmd.MarkFlagsMutuallyExclusive("source-dir", "metadata", "manifest")
	addTargetOrgFlag(cmd)
	return cmd
}

// deploySelection captures the mutually exclusive ways to choose what to deploy.
type deploySelection struct {
	sourceDir  string   // -d: a whole source-format directory
	metadata   []string // -m: Type or Type:Name specifiers
	manifest   string   // -x: a package.xml path
	projectDir string   // where to resolve -m/-x members from
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

func runDeploy(ctx context.Context, sel deploySelection, apiVersion string, dryRun bool, opts mdapi.DeployOptions) error {
	org, err := auth.Resolve(targetOrg)
	if err != nil {
		return err
	}
	client := mdapi.New(org)
	client.APIVersion = strings.TrimPrefix(apiVersion, "v")

	// The describe catalog drives type/verbatim classification; a failure here is
	// non-fatal — recompose falls back to its built-in heuristics.
	catalog, _, _ := client.DescribeMetadataCached(ctx, false)

	rec, err := recomposeSelection(sel, client.APIVersion, catalog)
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

// recomposeSelection turns the chosen selection into metadata-format entries and
// a manifest: a whole directory for -d, or project-resolved members for -m/-x.
func recomposeSelection(sel deploySelection, version string, catalog *mdapi.DescribeResult) (*source.RecomposeResult, error) {
	if sel.sourceDir != "" {
		return source.RecomposeDir(sel.sourceDir, version, catalog)
	}

	var pkg *mdapi.Package
	var err error
	if sel.manifest != "" {
		pkg, err = mdapi.LoadManifest(sel.manifest)
	} else {
		pkg, err = mdapi.ParseSpecifiers(sel.metadata, version)
	}
	if err != nil {
		return nil, err
	}

	start := sel.projectDir
	if start == "" {
		start = "."
	}
	proj, err := project.Find(start)
	if err != nil {
		return nil, err
	}
	return source.RecomposeMembers(proj, pkg, version, catalog)
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
