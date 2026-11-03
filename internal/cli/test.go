package cli

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/ko4edikov/sff/pkg/auth"
	"github.com/ko4edikov/sff/pkg/progress"
	"github.com/ko4edikov/sff/pkg/sfapi"
)

func newTestCmd() *cobra.Command {
	var classNames []string
	var testLevel, apiVersion string
	var coverage, asJSON bool
	var wait int
	cmd := &cobra.Command{
		Use:   "test",
		Short: "Run Apex tests in an org and report results and code coverage",
		Long: "Run Apex tests asynchronously via the Tooling API (like sf apex run test) and\n" +
			"print per-method results. Select tests with -n Class or -n Class.method\n" +
			"(repeatable, comma-separated allowed), or with -l RunLocalTests|RunAllTestsInOrg.\n" +
			"Add -c/--code-coverage to list line coverage of every class and trigger the\n" +
			"tests exercised, plus the org-wide percentage. Exits 1 if any test fails.",
		Example: `  sff test -n MyTest
  sff test -n MyTest.testInsert -n OtherTest -c
  sff test -l RunLocalTests --code-coverage
  sff test -n MyTest -c --json`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			req, err := buildTestRequest(classNames, testLevel)
			if err != nil {
				return err
			}
			if wait < 0 {
				return fmt.Errorf("--wait must be zero or positive (minutes)")
			}
			return runTests(cmd.Context(), req, apiVersion, coverage, asJSON, time.Duration(wait)*time.Minute)
		},
	}
	cmd.Flags().StringArrayVarP(&classNames, "class-names", "n", nil, "test class, or Class.method (repeatable, comma-separated allowed)")
	cmd.Flags().StringVarP(&testLevel, "test-level", "l", "", "RunLocalTests|RunAllTestsInOrg")
	cmd.Flags().BoolVarP(&coverage, "code-coverage", "c", false, "report code coverage of the classes the tests exercised")
	cmd.Flags().BoolVar(&asJSON, "json", false, "print the result as JSON")
	cmd.Flags().IntVarP(&wait, "wait", "w", 0, "minutes to wait for the run to finish (0 = wait indefinitely)")
	cmd.Flags().StringVar(&apiVersion, "api-version", "", apiVersionUsage)
	cmd.MarkFlagsMutuallyExclusive("class-names", "test-level")
	addTargetOrgFlag(cmd)
	return cmd
}

// buildTestRequest turns the -n/-l flags into a runner request. Class.method
// entries for the same class are merged; a bare Class entry runs the whole class.
func buildTestRequest(classNames []string, level string) (sfapi.ApexTestRequest, error) {
	if level != "" {
		switch strings.ToLower(strings.TrimSpace(level)) {
		case "runlocaltests":
			return sfapi.ApexTestRequest{TestLevel: "RunLocalTests"}, nil
		case "runalltestsinorg":
			return sfapi.ApexTestRequest{TestLevel: "RunAllTestsInOrg"}, nil
		}
		return sfapi.ApexTestRequest{}, fmt.Errorf("invalid --test-level %q (want RunLocalTests or RunAllTestsInOrg)", level)
	}
	var items []sfapi.ApexTestItem
	idx := map[string]int{}
	whole := map[string]bool{}
	for _, raw := range classNames {
		for _, spec := range strings.Split(raw, ",") {
			spec = strings.TrimSpace(spec)
			if spec == "" {
				continue
			}
			class, method, _ := strings.Cut(spec, ".")
			i, ok := idx[class]
			if !ok {
				i = len(items)
				idx[class] = i
				items = append(items, sfapi.ApexTestItem{ClassName: class})
			}
			if method == "" {
				whole[class] = true
			} else {
				items[i].TestMethods = append(items[i].TestMethods, method)
			}
		}
	}
	if len(items) == 0 {
		return sfapi.ApexTestRequest{}, fmt.Errorf("specify tests with -n Class[.method] or -l RunLocalTests")
	}
	for i := range items {
		if whole[items[i].ClassName] {
			items[i].TestMethods = nil
		}
	}
	return sfapi.ApexTestRequest{Tests: items}, nil
}

type testOutput struct {
	*sfapi.ApexTestRun
	Coverage        []sfapi.ApexClassCoverage `json:"coverage,omitempty"`
	OrgWideCoverage *float64                  `json:"orgWideCoverage,omitempty"`
}

func runTests(ctx context.Context, req sfapi.ApexTestRequest, apiVersion string, coverage, asJSON bool, wait time.Duration) error {
	org, err := auth.Resolve(targetOrg)
	if err != nil {
		return err
	}
	apiVersion = resolveAPIVersion(ctx, apiVersion, "", org)
	client := sfapi.New(org)
	if v := strings.TrimPrefix(apiVersion, "v"); v != "" {
		client.APIVersion = "v" + v
	}
	if wait > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, wait)
		defer cancel()
	}

	prog := progress.Start("running tests")
	run, err := client.RunApexTests(ctx, req, func(state string) {
		prog.Update("running tests — " + state)
	})
	if err == nil && coverage {
		prog.Update("collecting code coverage")
	}
	var out testOutput
	if err == nil {
		out.ApexTestRun = run
		if coverage {
			var ids []string
			for _, t := range run.Tests {
				ids = append(ids, t.ClassID)
			}
			var orgWide float64
			out.Coverage, orgWide, err = client.ApexCoverage(ctx, ids)
			if err == nil && orgWide >= 0 {
				out.OrgWideCoverage = &orgWide
			}
		}
	}
	prog.Stop()
	if err != nil {
		if errors.Is(err, context.DeadlineExceeded) {
			return fmt.Errorf("timed out after %s; the test run may still be going in %s", fmtDuration(wait), org.Username)
		}
		return err
	}

	failed := run.Failed > 0 || len(run.Errors) > 0
	if asJSON {
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		if err := enc.Encode(out); err != nil {
			return err
		}
	} else {
		printTestRun(out)
	}
	if failed {
		return &ExitError{Code: 1}
	}
	return nil
}

func printTestRun(out testOutput) {
	run := out.ApexTestRun
	for _, t := range run.Tests {
		mark := "✔"
		switch t.Outcome {
		case "Pass":
		case "Skip":
			mark = "-"
		default:
			mark = "✘"
		}
		fmt.Printf("%s %s.%s (%d ms)\n", mark, t.ClassName, t.Method, t.RunTimeMs)
		if mark == "✘" {
			if t.Message != "" {
				fmt.Printf("    %s\n", strings.ReplaceAll(strings.TrimSpace(t.Message), "\n", "\n    "))
			}
			if t.StackTrace != "" {
				fmt.Printf("    %s\n", strings.ReplaceAll(strings.TrimSpace(t.StackTrace), "\n", "\n    "))
			}
		}
	}
	for _, e := range run.Errors {
		fmt.Printf("✘ %s\n", e)
	}
	fmt.Printf("\n%d passed, %d failed, %d skipped in %s\n", run.Passed, run.Failed, run.Skipped, fmtDuration(run.Duration))

	if out.Coverage != nil || out.OrgWideCoverage != nil {
		fmt.Println("\nCode coverage:")
		var cov, unc int
		for _, c := range out.Coverage {
			cov += c.LinesCovered
			unc += c.LinesUncovered
			fmt.Printf("  %6.2f%%  %-40s %d/%d lines\n", c.Percent, c.Name, c.LinesCovered, c.LinesCovered+c.LinesUncovered)
		}
		if cov+unc > 0 {
			fmt.Printf("  %6.2f%%  %-40s %d/%d lines\n", float64(cov)*100/float64(cov+unc), "TOTAL (classes above)", cov, cov+unc)
		}
		if out.OrgWideCoverage != nil {
			fmt.Printf("  %6.2f%%  org-wide\n", *out.OrgWideCoverage)
		}
	}
}
