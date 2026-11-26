package cli

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/ko4edikov/sff/pkg/auth"
	"github.com/ko4edikov/sff/pkg/gitchange"
	"github.com/ko4edikov/sff/pkg/progress"
	"github.com/ko4edikov/sff/pkg/sfapi"
)

func newTestCmd() *cobra.Command {
	var classNames []string
	var commit string
	var testLevel, apiVersion string
	var coverage, detailed, concise, asJSON, dryRun, changed bool
	var wait int
	cmd := &cobra.Command{
		Use:   "test",
		Short: "Run Apex tests in an org and report results and code coverage",
		Long: "Run Apex tests asynchronously via the Tooling API (like sf apex run test) and\n" +
			"print per-method results. Select tests with -n Class or -n Class.method\n" +
			"(class names may contain * wildcards, e.g. -n 'Account*Test')\n" +
			"(repeatable, comma-separated allowed), or with -l RunLocalTests|RunAllTestsInOrg.\n" +
			"Add -c/--code-coverage to list line coverage of every class and trigger the\n" +
			"tests exercised (with uncovered lines), plus the org-wide percentage;\n" +
			"--detailed-coverage adds per-test-method coverage per class, --concise limits the\n" +
			"final report to failures and the summary. Exits 1 if any test fails.\n\n" +
			"Use --changed to run the test classes (@IsTest) added or modified in the git\n" +
			"working tree vs HEAD (staged, unstaged, untracked), like sff deploy --changed, or\n" +
			"--commit <sha> for the test classes one commit added or modified.",
		Example: `  sff test -n MyTest
  sff test -n MyTest.testInsert -n OtherTest -c
  sff test -n 'Account*Test' -c
  sff test -n 'Account*Test' --dry-run
  sff test --changed -c
  sff test --commit HEAD~1 -c
  sff test -l RunLocalTests --code-coverage
  sff test -n MyTest -c --detailed-coverage
  sff test -n MyTest --concise
  sff test -n MyTest -c --json`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			var req sfapi.ApexTestRequest
			var err error
			if changed || commit != "" {
				req, err = changedTestRequest("", commit)
			} else {
				req, err = buildTestRequest(classNames, testLevel)
			}
			if err != nil {
				return err
			}
			if detailed && !coverage {
				return fmt.Errorf("--detailed-coverage requires -c/--code-coverage")
			}
			if wait < 0 {
				return fmt.Errorf("--wait must be zero or positive (minutes)")
			}
			return runTests(cmd.Context(), req, apiVersion, testReport{Coverage: coverage, Detailed: detailed, Concise: concise}, asJSON, dryRun, time.Duration(wait)*time.Minute)
		},
	}
	cmd.Flags().StringArrayVarP(&classNames, "class-names", "n", nil, "test class, or Class.method (repeatable, comma-separated allowed)")
	cmd.Flags().StringVarP(&testLevel, "test-level", "l", "", "RunLocalTests|RunAllTestsInOrg")
	cmd.Flags().BoolVarP(&coverage, "code-coverage", "c", false, "report code coverage of the classes the tests exercised")
	cmd.Flags().BoolVar(&detailed, "detailed-coverage", false, "with -c: also show, per test method, the coverage it achieved on each class (like sf)")
	cmd.Flags().BoolVar(&concise, "concise", false, "print only failing tests (and the summary) in the final report")
	cmd.Flags().BoolVar(&asJSON, "json", false, "print the result as JSON")
	cmd.Flags().BoolVar(&dryRun, "dry-run", false, "resolve the tests to run (expanding * wildcards) and print them without running")
	cmd.Flags().BoolVar(&changed, "changed", false, "run the test classes changed in the git working tree vs HEAD (staged, unstaged, untracked)")
	cmd.Flags().IntVarP(&wait, "wait", "w", 0, "minutes to wait for the run to finish (0 = wait indefinitely)")
	cmd.Flags().StringVar(&apiVersion, "api-version", "", apiVersionUsage)
	cmd.Flags().StringVar(&commit, "commit", "", "run the test classes (@IsTest) a git commit added or modified (vs its first parent)")
	cmd.MarkFlagsMutuallyExclusive("class-names", "test-level", "changed", "commit")
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

var isTestRe = regexp.MustCompile(`(?i)@istest\b`)

// changedTestRequest builds a runner request from the @IsTest Apex classes that
// are added or modified in the git working tree (vs HEAD). Deleted files and
// non-test classes are skipped. projectDir is where to look for the project
// ("" = current directory). With commit set, the commit's own changes are used
// (and @IsTest is checked against the file as of that commit).
func changedTestRequest(projectDir, commit string) (sfapi.ApexTestRequest, error) {
	cs, _, err := resolveGitChanges(deploySelection{changed: commit == "", commit: commit, projectDir: projectDir}, "", nil)
	if err != nil {
		return sfapi.ApexTestRequest{}, err
	}
	var items []sfapi.ApexTestItem
	for _, p := range cs.Paths {
		p = strings.TrimSuffix(p, "-meta.xml") // a changed Foo.cls-meta.xml still means class Foo
		if !strings.HasSuffix(p, ".cls") {
			continue
		}
		var data []byte
		if commit != "" {
			var ok bool
			if data, ok = gitchange.ShowAt(filepath.Dir(p), commit, p); !ok {
				continue
			}
		} else if data, err = os.ReadFile(p); err != nil {
			continue
		}
		if !isTestRe.Match(data) {
			continue
		}
		items = append(items, sfapi.ApexTestItem{ClassName: strings.TrimSuffix(filepath.Base(p), ".cls")})
	}
	if len(items) == 0 {
		return sfapi.ApexTestRequest{}, fmt.Errorf("no changed Apex test classes (@IsTest) found in the git change set")
	}
	return sfapi.ApexTestRequest{Tests: items}, nil
}

// likePattern converts a '*' wildcard class name into a SOQL LIKE pattern,
// escaping the characters that are special in LIKE or string literals.
func likePattern(name string) string {
	var b strings.Builder
	for _, r := range name {
		switch r {
		case '*':
			b.WriteByte('%')
		case '%', '_', '\\', '\'':
			b.WriteByte('\\')
			b.WriteRune(r)
		default:
			b.WriteRune(r)
		}
	}
	return b.String()
}

// mergeTestItems appends item to items, merging with an existing entry for the
// same class (a whole-class entry, i.e. no methods, wins).
func mergeTestItems(items []sfapi.ApexTestItem, idx map[string]int, item sfapi.ApexTestItem) []sfapi.ApexTestItem {
	i, ok := idx[item.ClassName]
	if !ok {
		idx[item.ClassName] = len(items)
		return append(items, item)
	}
	if len(items[i].TestMethods) == 0 || len(item.TestMethods) == 0 {
		items[i].TestMethods = nil
	} else {
		items[i].TestMethods = append(items[i].TestMethods, item.TestMethods...)
	}
	return items
}

// expandWildcardTests replaces class names containing '*' with the matching
// local (non-namespaced) Apex classes, queried via the Tooling API. A method
// part (Pattern*.method) is applied to every match.
func expandWildcardTests(ctx context.Context, client *sfapi.Client, items []sfapi.ApexTestItem) ([]sfapi.ApexTestItem, error) {
	wild := false
	for _, it := range items {
		if strings.Contains(it.ClassName, "*") {
			wild = true
			break
		}
	}
	if !wild {
		return items, nil
	}
	var out []sfapi.ApexTestItem
	idx := map[string]int{}
	for _, it := range items {
		if !strings.Contains(it.ClassName, "*") {
			out = mergeTestItems(out, idx, it)
			continue
		}
		soql := "SELECT Name FROM ApexClass WHERE NamespacePrefix = null AND Name LIKE '" +
			likePattern(it.ClassName) + "' ORDER BY Name"
		recs, _, err := client.QueryTooling(ctx, soql)
		if err != nil {
			return nil, fmt.Errorf("expanding %q: %w", it.ClassName, err)
		}
		if len(recs) == 0 {
			return nil, fmt.Errorf("no Apex class matches %q", it.ClassName)
		}
		for _, raw := range recs {
			var r struct{ Name string }
			if err := json.Unmarshal(raw, &r); err != nil {
				return nil, err
			}
			out = mergeTestItems(out, idx, sfapi.ApexTestItem{
				ClassName:   r.Name,
				TestMethods: append([]string(nil), it.TestMethods...),
			})
		}
	}
	return out, nil
}

// testReport selects which tables the final report contains.
type testReport struct {
	Coverage bool // -c
	Detailed bool // --detailed-coverage
	Concise  bool // --concise
}

type testOutput struct {
	*sfapi.ApexTestRun
	Coverage        []sfapi.ApexClassCoverage `json:"coverage,omitempty"`
	PerTestCoverage []sfapi.ApexTestCoverage  `json:"perTestCoverage,omitempty"`
	OrgWideCoverage *float64                  `json:"orgWideCoverage,omitempty"`
}

func runTests(ctx context.Context, req sfapi.ApexTestRequest, apiVersion string, rep testReport, asJSON, dryRun bool, wait time.Duration) error {
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

	expanded := false
	for _, it := range req.Tests {
		if strings.Contains(it.ClassName, "*") {
			expanded = true
		}
	}
	req.Tests, err = expandWildcardTests(ctx, client, req.Tests)
	if err != nil {
		return err
	}
	if dryRun || expanded {
		// stderr keeps --json output on stdout clean
		if req.TestLevel != "" {
			fmt.Fprintf(os.Stderr, "Test level: %s\n", req.TestLevel)
		} else {
			fmt.Fprintf(os.Stderr, "Tests to run (%d classes):\n", len(req.Tests))
			for _, it := range req.Tests {
				if len(it.TestMethods) == 0 {
					fmt.Fprintf(os.Stderr, "  %s\n", it.ClassName)
				} else {
					fmt.Fprintf(os.Stderr, "  %s.%s\n", it.ClassName, strings.Join(it.TestMethods, ","))
				}
			}
		}
	}
	if dryRun {
		return nil
	}

	prog := progress.Start("running tests")
	hooks := &sfapi.ApexTestHooks{
		Progress: func(state string) { prog.Update("running tests — " + state) },
	}
	streamed := !asJSON
	if streamed {
		hooks.Queued = func(classes []string) {
			var b strings.Builder
			fmt.Fprintf(&b, "Running %d test class(es):\n", len(classes))
			for _, c := range classes {
				fmt.Fprintf(&b, "  %s\n", c)
			}
			prog.Print(os.Stdout, b.String())
		}
	}
	run, err := client.RunApexTests(ctx, req, hooks)
	if err == nil && rep.Coverage {
		prog.Update("collecting code coverage")
	}
	var out testOutput
	if err == nil {
		out.ApexTestRun = run
		if rep.Coverage {
			names := map[string]string{}
			for _, t := range run.Tests {
				names[t.ClassID] = t.ClassName
			}
			var cr *sfapi.ApexCoverageReport
			cr, err = client.ApexCoverage(ctx, names)
			if cr != nil {
				out.Coverage = cr.Classes
				if rep.Detailed {
					out.PerTestCoverage = cr.PerTest
				}
				if cr.OrgWide >= 0 {
					out.OrgWideCoverage = &cr.OrgWide
				}
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
		printTestRun(os.Stdout, out, rep, org, termWidth())
	}
	if failed {
		return &ExitError{Code: 1}
	}
	return nil
}

// pctStr renders a coverage percentage like sf: whole numbers, never "100%"
// unless every line is covered.
func pctStr(covered, uncovered int) string {
	if uncovered == 0 {
		return "100%"
	}
	p := int(float64(covered)*100/float64(covered+uncovered) + 0.5)
	if p > 99 {
		p = 99
	}
	return fmt.Sprintf("%d%%", p)
}

func uncoveredStr(lines []int) string {
	const limit = 5
	var b strings.Builder
	for i, n := range lines {
		if i == limit {
			b.WriteString(",...")
			break
		}
		if i > 0 {
			b.WriteString(",")
		}
		fmt.Fprintf(&b, "%d", n)
	}
	return b.String()
}

func testMessage(t sfapi.ApexTestOutcome) string {
	if t.StackTrace != "" {
		return t.Message + "\n" + t.StackTrace
	}
	return t.Message
}

func isTestFailure(t sfapi.ApexTestOutcome) bool { return t.Outcome != "Pass" && t.Outcome != "Skip" }

func ratePct(n, total int) string {
	if total == 0 {
		return "0%"
	}
	return fmt.Sprintf("%d%%", int(float64(n)*100/float64(total)+0.5))
}

// printTestRun prints the final report in the layout of sf apex run test:
// Test Results (or the detailed per-test coverage), coverage by class, summary.
func printTestRun(w io.Writer, out testOutput, rep testReport, org *auth.Org, width int) {
	run := out.ApexTestRun
	for _, e := range run.Errors {
		fmt.Fprintf(w, "✘ %s\n", e)
	}

	// Per-test rows (concise keeps failures only).
	var tests []sfapi.ApexTestOutcome
	for _, t := range run.Tests {
		if !rep.Concise || isTestFailure(t) {
			tests = append(tests, t)
		}
	}
	fmt.Fprintln(w)
	if rep.Detailed && out.Coverage != nil {
		byTest := map[string][]sfapi.ApexTestCoverage{}
		for _, c := range out.PerTestCoverage {
			k := c.TestClass + "." + c.TestMethod
			byTest[k] = append(byTest[k], c)
		}
		var rows [][]string
		for _, t := range tests {
			name := t.ClassName + "." + t.Method
			covs := byTest[name]
			if len(covs) == 0 {
				rows = append(rows, []string{name, "", t.Outcome, "", testMessage(t), fmt.Sprint(t.RunTimeMs)})
			}
			for _, c := range covs {
				rows = append(rows, []string{name, c.Target, t.Outcome, pctStr(c.LinesCovered, c.LinesUncovered), testMessage(t), fmt.Sprint(t.RunTimeMs)})
			}
		}
		if len(rows) > 0 {
			printTable(w, "Apex Code Coverage for Test Run "+run.JobID,
				[]string{"TEST NAME", "CLASS BEING TESTED", "OUTCOME", "PERCENT", "MESSAGE", "RUNTIME (MS)"}, rows, width)
			fmt.Fprint(w, "\n\n")
		}
	} else if len(tests) > 0 {
		var rows [][]string
		for _, t := range tests {
			rows = append(rows, []string{t.ClassName + "." + t.Method, t.Outcome, testMessage(t), fmt.Sprint(t.RunTimeMs)})
		}
		printTable(w, "Test Results", []string{"TEST NAME", "OUTCOME", "MESSAGE", "RUNTIME (MS)"}, rows, width)
		fmt.Fprint(w, "\n\n")
	}

	if out.Coverage != nil && !rep.Concise {
		var rows [][]string
		for _, c := range out.Coverage {
			rows = append(rows, []string{c.Name, pctStr(c.LinesCovered, c.LinesUncovered), uncoveredStr(c.UncoveredLines)})
		}
		printTable(w, "Apex Code Coverage by Class", []string{"CLASSES", "PERCENT", "UNCOVERED LINES"}, rows, width)
		fmt.Fprint(w, "\n\n")
	}

	ran := len(run.Tests)
	outcome := "Passed"
	if run.Failed > 0 || len(run.Errors) > 0 {
		outcome = "Failed"
	}
	rows := [][]string{
		{"Outcome", outcome},
		{"Tests Ran", fmt.Sprint(ran)},
		{"Pass Rate", ratePct(run.Passed, ran)},
		{"Fail Rate", ratePct(run.Failed, ran)},
		{"Skip Rate", ratePct(run.Skipped, ran)},
		{"Test Run Id", run.JobID},
		{"Test Setup Time", fmt.Sprintf("%d ms", run.SetupTimeMs)},
		{"Test Execution Time", fmt.Sprintf("%d ms", run.TestTimeMs)},
		{"Test Total Time", fmt.Sprintf("%d ms", run.SetupTimeMs+run.TestTimeMs)},
		{"Org Id", org.OrgID},
		{"Username", org.Username},
	}
	if out.OrgWideCoverage != nil {
		rows = append(rows, []string{"Org Wide Coverage", fmt.Sprintf("%d%%", int(*out.OrgWideCoverage+0.5))})
	}
	printTable(w, "Test Summary", []string{"NAME", "VALUE"}, rows, width)
}
