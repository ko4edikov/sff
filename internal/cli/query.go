package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/spf13/cobra"

	"github.com/ko4edikov/sff/internal/auth"
	"github.com/ko4edikov/sff/internal/sfapi"
)

func newQueryCmd() *cobra.Command {
	var asJSON bool
	cmd := &cobra.Command{
		Use:   "query <soql>",
		Short: "Run a SOQL query",
		Long:  "Run a SOQL query against an org and print the results as a table (or raw JSON with --json).",
		Example: `  sff query "SELECT Id, Name FROM Account LIMIT 10"
  sff query "SELECT Id FROM Contact" -o pr-dev --json`,
		Args: cobra.ArbitraryArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			soql := strings.TrimSpace(strings.Join(args, " "))
			if soql == "" {
				return fmt.Errorf("a SOQL statement is required")
			}
			return runQuery(cmd.Context(), soql, asJSON)
		},
	}
	cmd.Flags().BoolVar(&asJSON, "json", false, "print raw JSON records instead of a table")
	return cmd
}

func runQuery(ctx context.Context, soql string, asJSON bool) error {
	org, err := auth.Resolve(targetOrg)
	if err != nil {
		return err
	}

	start := time.Now()
	records, total, err := sfapi.New(org).Query(ctx, soql)
	elapsed := time.Since(start)
	if err != nil {
		return err
	}

	if asJSON {
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		if err := enc.Encode(records); err != nil {
			return err
		}
		fmt.Fprintf(os.Stderr, "queried in %s\n", fmtDuration(elapsed))
		return nil
	}
	return printTable(records, total, elapsed)
}

// fmtDuration renders a duration with millisecond precision.
func fmtDuration(d time.Duration) string {
	return d.Round(time.Millisecond).String()
}

// printTable renders records as a column-aligned table, preserving SELECT order.
func printTable(records []json.RawMessage, total int, elapsed time.Duration) error {
	if len(records) == 0 {
		// A COUNT() query returns no records, only totalSize.
		if total > 0 {
			fmt.Printf("count: %d\n", total)
		} else {
			fmt.Println("No records found.")
		}
		fmt.Printf("(%s)\n", fmtDuration(elapsed))
		return nil
	}
	cols, err := sfapi.Columns(records[0])
	if err != nil {
		return err
	}

	tw := tabwriter.NewWriter(os.Stdout, 0, 2, 2, ' ', 0)
	fmt.Fprintln(tw, strings.Join(cols, "\t"))
	seps := make([]string, len(cols))
	for i, c := range cols {
		seps[i] = strings.Repeat("─", len([]rune(c)))
	}
	fmt.Fprintln(tw, strings.Join(seps, "\t"))

	for _, rec := range records {
		vals := make([]string, len(cols))
		for i, c := range cols {
			vals[i] = sfapi.Field(rec, c)
		}
		fmt.Fprintln(tw, strings.Join(vals, "\t"))
	}
	if err := tw.Flush(); err != nil {
		return err
	}
	fmt.Printf("\n%d row(s) returned (totalSize %d) in %s\n", len(records), total, fmtDuration(elapsed))
	return nil
}
