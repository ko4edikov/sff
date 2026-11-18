package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"sort"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/spf13/cobra"
	"golang.org/x/term"

	"github.com/ko4edikov/sff/pkg/auth"
	"github.com/ko4edikov/sff/pkg/mdapi"
	"github.com/ko4edikov/sff/pkg/sfapi"
)

// deployListFlags are the filters shared by `deploy list` and the interactive
// picker of `deploy report`.
type deployListFlags struct {
	limit                int
	status, since, until string
	mine, checkOnly      bool
}

func addDeployListFlags(cmd *cobra.Command, f *deployListFlags, defLimit int) {
	cmd.Flags().IntVarP(&f.limit, "limit", "n", defLimit, "max deploys to list (newest first)")
	cmd.Flags().StringVar(&f.status, "status", "", "only deploys with this status: Succeeded, Failed, InProgress, Canceled, SucceededPartial, Pending…")
	cmd.Flags().StringVar(&f.since, "since", "", "only deploys started since: 30m, 2h, 3d, 1w, today, yesterday or a date (2026-11-01)")
	cmd.Flags().StringVar(&f.until, "until", "", "only deploys started up to this time (same formats as --since)")
	cmd.Flags().BoolVar(&f.mine, "mine", false, "only deploys started by the target org's user")
	cmd.Flags().BoolVar(&f.checkOnly, "check-only", false, "only validations (check-only deploys)")
}

// fetch lists the deploys matching the flags.
func (f *deployListFlags) fetch(ctx context.Context, org *auth.Org) ([]sfapi.DeployRequest, error) {
	o := sfapi.DeployListOptions{Limit: f.limit, Status: f.status}
	now := time.Now()
	var err error
	if f.since != "" {
		if o.Since, err = parseWhen(f.since, now); err != nil {
			return nil, fmt.Errorf("--since: %w", err)
		}
	}
	if f.until != "" {
		if o.Until, err = parseWhen(f.until, now); err != nil {
			return nil, fmt.Errorf("--until: %w", err)
		}
	}
	if f.mine {
		o.Username = org.Username
	}
	if f.checkOnly {
		o.CheckOnly = "true"
	}
	return sfapi.New(org).ListDeployRequests(ctx, o)
}

func newDeployListCmd() *cobra.Command {
	var f deployListFlags
	var asJSON bool
	cmd := &cobra.Command{
		Use:   "list",
		Short: "List recent deploys (Setup → Deployment Status)",
		Long: "List the org's recent deploys and validations, newest first — the same\n" +
			"data as Setup → Deployment Status (Salesforce keeps about 180 days). Use the\n" +
			"Id with `sff deploy report <id>` to see what each deploy did per component.",
		Example: `  sff deploy list
  sff deploy list --mine --since 3d
  sff deploy list --status Failed -n 50 -o pr-dev
  sff deploy list --check-only`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			org, err := auth.Resolve(targetOrg)
			if err != nil {
				return err
			}
			deploys, err := f.fetch(cmd.Context(), org)
			if err != nil {
				return err
			}
			if asJSON {
				enc := json.NewEncoder(os.Stdout)
				enc.SetIndent("", "  ")
				return enc.Encode(deploys)
			}
			if len(deploys) == 0 {
				fmt.Println("No deploys found.")
				return nil
			}
			tw := tabwriter.NewWriter(os.Stdout, 0, 2, 2, ' ', 0)
			fmt.Fprintln(tw, "ID\tSTATUS\tKIND\tSTARTED\tTOOK\tBY\tCOMPONENTS\tTESTS")
			for _, d := range deploys {
				fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\t%s\t%s\t%s\n", d.ID, d.Status, deployKind(d),
					d.Created.Local().Format("2006-01-02 15:04"), deployTook(d), d.CreatedBy, deployComponents(d), deployTests(d))
			}
			return tw.Flush()
		},
	}
	addTargetOrgFlag(cmd)
	addDeployListFlags(cmd, &f, 20)
	cmd.Flags().BoolVar(&asJSON, "json", false, "output JSON")
	return cmd
}

func newDeployReportCmd() *cobra.Command {
	var f deployListFlags
	var apiVersion string
	var withTests bool
	cmd := &cobra.Command{
		Use:   "report [deploy-id]",
		Short: "Show the per-component result of a past deploy",
		Long: "Show what a deploy did to each component (Created, Changed, Deleted or\n" +
			"Unchanged) plus any component errors, for a deploy Id from `sff deploy list`.\n" +
			"With no Id in an interactive terminal, pick the deploy from a list (use the\n" +
			"same filters as `deploy list`); the report is printed on Enter.",
		Example: `  sff deploy report 0AfG500000Taw4nKAB
  sff deploy report --tests <id>    # + test results and coverage
  sff deploy report                 # choose from a list
  sff deploy report --mine --since 1d`,
		Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			org, err := auth.Resolve(targetOrg)
			if err != nil {
				return err
			}
			ctx := cmd.Context()
			var id string
			if len(args) == 1 {
				id = args[0]
			} else {
				if !term.IsTerminal(int(os.Stdin.Fd())) || !term.IsTerminal(int(os.Stderr.Fd())) {
					return fmt.Errorf("specify a deploy Id (see `sff deploy list`), or run in a terminal to pick one")
				}
				deploys, err := f.fetch(ctx, org)
				if err != nil {
					return err
				}
				rows := make([]string, len(deploys))
				for i, d := range deploys {
					rows[i] = fmt.Sprintf("%-18s %-17s %-8s %s  %-6s %-22s %s %s", d.ID, d.Status, deployKind(d),
						d.Created.Local().Format("2006-01-02 15:04"), deployTook(d), d.CreatedBy, deployComponents(d), deployTests(d))
				}
				hdr := fmt.Sprintf("%-18s %-17s %-8s %-16s  %-6s %-22s %s", "ID", "STATUS", "KIND", "STARTED", "TOOK", "BY", "COMPONENTS")
				i, err := pickRow("Pick a deploy to report on — "+org.Username, hdr, rows)
				if err == errPickCancelled {
					return nil
				}
				if err != nil {
					return err
				}
				id = deploys[i].ID
			}
			return runDeployReport(ctx, org, id, apiVersion, withTests)
		},
	}
	addTargetOrgFlag(cmd)
	addDeployListFlags(cmd, &f, 30)
	cmd.Flags().StringVar(&apiVersion, "api-version", "", apiVersionUsage)
	cmd.Flags().BoolVar(&withTests, "tests", false, "also show Apex test results and code coverage")
	return cmd
}

func runDeployReport(ctx context.Context, org *auth.Org, id, apiVersion string, withTests bool) error {
	apiVersion = resolveAPIVersion(ctx, apiVersion, "", org)
	client := newMDClient(org)
	client.APIVersion = strings.TrimPrefix(apiVersion, "v")
	_, res, err := client.CheckDeployStatus(ctx, id)
	if err != nil {
		return err
	}
	kind := "deploy"
	if res.CheckOnly {
		kind = "validation"
	}
	fmt.Printf("%s %s: %s (%d/%d components, %d error(s))\n", kind, res.ID, res.Status,
		res.NumberComponentsDeployed, res.NumberComponentsTotal, res.NumberComponentErrors)
	if res.NumberTestsTotal > 0 {
		fmt.Printf("tests: %d/%d run, %d failed\n", res.NumberTestsCompleted, res.NumberTestsTotal, res.NumberTestErrors)
	}
	printDeploySuccesses(os.Stdout, res.ComponentSuccesses)
	printDeployFailures(res)
	if withTests {
		printDeployTests(os.Stdout, res)
	}
	if !res.Success && res.Done {
		return &ExitError{Code: 1}
	}
	return nil
}

func deployKind(d sfapi.DeployRequest) string {
	if d.CheckOnly {
		return "validate"
	}
	return "deploy"
}

func deployTook(d sfapi.DeployRequest) string {
	if d.Completed.IsZero() || d.Created.IsZero() {
		return "-"
	}
	return d.Completed.Sub(d.Created).Round(time.Second).String()
}

func deployComponents(d sfapi.DeployRequest) string {
	s := fmt.Sprintf("%d/%d", d.ComponentsDeployed, d.ComponentsTotal)
	if d.ComponentErrors > 0 {
		s += fmt.Sprintf(" (%d err)", d.ComponentErrors)
	}
	return s
}

func deployTests(d sfapi.DeployRequest) string {
	if d.TestsTotal == 0 {
		return "-"
	}
	s := fmt.Sprintf("%d/%d", d.TestsCompleted, d.TestsTotal)
	if d.TestErrors > 0 {
		s += fmt.Sprintf(" (%d fail)", d.TestErrors)
	}
	return s
}

// printDeployTests prints the Apex test outcomes and per-class code coverage of
// a deploy that ran tests (failures first, then passes; coverage worst first).
func printDeployTests(w io.Writer, res *mdapi.DeployResult) {
	if res.NumberTestsTotal == 0 && len(res.Coverage) == 0 {
		fmt.Fprintln(w, "\nno tests were run in this deploy")
		return
	}
	tw := tabwriter.NewWriter(w, 0, 2, 2, ' ', 0)
	fmt.Fprintln(tw, "\nTEST\tMETHOD\tRESULT\tTIME")
	for _, t := range res.TestFailures {
		fmt.Fprintf(tw, "%s\t%s\tFail\t\n", t.Name, t.MethodName)
	}
	for _, t := range res.TestSuccesses {
		fmt.Fprintf(tw, "%s\t%s\tPass\t%s\n", t.Name, t.MethodName, (time.Duration(t.Time) * time.Millisecond).String())
	}
	tw.Flush()
	if len(res.Coverage) > 0 {
		cov := append([]mdapi.CodeCoverage(nil), res.Coverage...)
		sort.Slice(cov, func(i, j int) bool { return cov[i].Percent() < cov[j].Percent() })
		tw = tabwriter.NewWriter(w, 0, 2, 2, ' ', 0)
		fmt.Fprintln(tw, "\nCLASS\tTYPE\tCOVERAGE\tLINES")
		for _, c := range cov {
			fmt.Fprintf(tw, "%s\t%s\t%.0f%%\t%d/%d\n", c.Name, c.Type, c.Percent(), c.NumLocations-c.NumLocationsNotCovered, c.NumLocations)
		}
		tw.Flush()
	}
	for _, cw := range res.CoverageWarnings {
		fmt.Fprintf(w, "coverage warning: %s %s\n", cw.Name, cw.Message)
	}
}
