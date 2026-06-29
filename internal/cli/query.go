package cli

import (
	"context"
	"encoding/csv"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/spf13/cobra"

	"github.com/ko4edikov/sff/pkg/auth"
	"github.com/ko4edikov/sff/pkg/sfapi"
)

func newQueryCmd() *cobra.Command {
	var asJSON, asCSV, useTooling bool
	var outFile string
	var concurrency int
	cmd := &cobra.Command{
		Use:   "query <soql>",
		Short: "Run a SOQL query",
		Long:  "Run a SOQL query against an org and print the results as a table, JSON, or CSV.",
		Example: `  sff query "SELECT Id, Name FROM Account LIMIT 10"
  sff query "SELECT Id FROM Contact" -o pr-dev --json
  sff query "SELECT Id, Name FROM ApexClass" -t
  sff query "SELECT Id, Name FROM Account" --csv -f accounts.csv`,
		Args: cobra.ArbitraryArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			soql := strings.TrimSpace(strings.Join(args, " "))
			if soql == "" {
				return fmt.Errorf("a SOQL statement is required")
			}
			format := formatTable
			if asJSON {
				format = formatJSON
			} else if asCSV {
				format = formatCSV
			}
			return runQuery(cmd.Context(), soql, format, outFile, useTooling, concurrency)
		},
	}
	cmd.Flags().BoolVar(&asJSON, "json", false, "output raw JSON records")
	cmd.Flags().BoolVar(&asCSV, "csv", false, "output CSV")
	cmd.Flags().StringVarP(&outFile, "out-file", "f", "", "write the result to a file instead of stdout")
	cmd.Flags().BoolVarP(&useTooling, "use-tooling-api", "t", false, "query the Tooling API (e.g. ApexClass, Flow, CustomField)")
	cmd.Flags().IntVarP(&concurrency, "concurrency", "c", sfapi.DefaultBatchConcurrency, "max parallel composite/batch calls when paginating large results")
	cmd.MarkFlagsMutuallyExclusive("json", "csv")
	addTargetOrgFlag(cmd)
	return cmd
}

type outFormat int

const (
	formatTable outFormat = iota
	formatJSON
	formatCSV
)

func runQuery(ctx context.Context, soql string, format outFormat, outFile string, useTooling bool, concurrency int) error {
	org, err := auth.Resolve(targetOrg)
	if err != nil {
		return err
	}

	client := sfapi.New(org)
	client.BatchConcurrency = concurrency
	queryFn := client.Query
	if useTooling {
		queryFn = client.QueryTooling
	}

	prog := startProgress("querying " + org.Username)
	start := time.Now()
	records, total, err := queryFn(ctx, soql)
	elapsed := time.Since(start)
	prog.Stop()
	if err != nil {
		return err
	}

	// Pick the data sink: a file, or stdout.
	w := io.Writer(os.Stdout)
	if outFile != "" {
		f, err := os.Create(outFile)
		if err != nil {
			return fmt.Errorf("create %s: %w", outFile, err)
		}
		defer f.Close()
		w = f
	}

	switch format {
	case formatJSON:
		err = writeJSON(w, records, total)
	case formatCSV:
		err = writeCSV(w, records, total)
	default:
		err = writeTable(w, records, total)
	}
	if err != nil {
		return err
	}

	// The summary goes to stdout only for a table printed to the terminal;
	// otherwise it goes to stderr so it never pollutes piped/saved data.
	summary := io.Writer(os.Stderr)
	if outFile == "" && format == formatTable {
		summary = os.Stdout
	}
	if outFile != "" {
		fmt.Fprintf(summary, "wrote %d record(s) to %s in %s\n", len(records), outFile, fmtDuration(elapsed))
	} else {
		fmt.Fprintf(summary, "%s%d row(s) returned (totalSize %d) in %s\n", summarySep(format), len(records), total, fmtDuration(elapsed))
	}
	return nil
}

// summarySep adds a blank line before the table's summary for readability.
func summarySep(format outFormat) string {
	if format == formatTable {
		return "\n"
	}
	return ""
}

// sfQueryJSON mirrors `sf data query --json` so sff is a drop-in replacement
// for tooling that parses sf's output (`.result.records`, etc.).
type sfQueryJSON struct {
	Status int           `json:"status"`
	Result sfQueryResult `json:"result"`
}

type sfQueryResult struct {
	Records   []json.RawMessage `json:"records"`
	TotalSize int               `json:"totalSize"`
	Done      bool              `json:"done"`
}

func writeJSON(w io.Writer, records []json.RawMessage, total int) error {
	if records == nil {
		records = []json.RawMessage{} // match sf: empty array, not null
	}
	out := sfQueryJSON{
		Status: 0,
		Result: sfQueryResult{Records: records, TotalSize: total, Done: true},
	}
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	return enc.Encode(out)
}

// writeCSV writes records as RFC 4180 CSV with a header row in SELECT order.
func writeCSV(w io.Writer, records []json.RawMessage, total int) error {
	cw := csv.NewWriter(w)
	if len(records) == 0 {
		if total > 0 { // COUNT() query
			_ = cw.Write([]string{"count"})
			_ = cw.Write([]string{strconv.Itoa(total)})
		}
		cw.Flush()
		return cw.Error()
	}
	cols, err := sfapi.Columns(records[0])
	if err != nil {
		return err
	}
	if err := cw.Write(cols); err != nil {
		return err
	}
	for _, rec := range records {
		row := make([]string, len(cols))
		for i, c := range cols {
			row[i] = sfapi.Field(rec, c)
		}
		if err := cw.Write(row); err != nil {
			return err
		}
	}
	cw.Flush()
	return cw.Error()
}

// fmtDuration renders a duration with millisecond precision.
func fmtDuration(d time.Duration) string {
	return d.Round(time.Millisecond).String()
}

// writeTable renders records as a column-aligned table, preserving SELECT order.
func writeTable(w io.Writer, records []json.RawMessage, total int) error {
	if len(records) == 0 {
		// A COUNT() query returns no records, only totalSize.
		if total > 0 {
			fmt.Fprintf(w, "count: %d\n", total)
		} else {
			fmt.Fprintln(w, "No records found.")
		}
		return nil
	}
	cols, err := sfapi.Columns(records[0])
	if err != nil {
		return err
	}

	// Prepend a 1-based row-number column ("#") to the human-readable table.
	// CSV/JSON output is left untouched so it stays a drop-in for sf consumers.
	header := append([]string{"#"}, cols...)

	tw := tabwriter.NewWriter(w, 0, 2, 2, ' ', 0)
	fmt.Fprintln(tw, strings.Join(header, "\t"))
	seps := make([]string, len(header))
	for i, c := range header {
		seps[i] = strings.Repeat("─", len([]rune(c)))
	}
	fmt.Fprintln(tw, strings.Join(seps, "\t"))

	for idx, rec := range records {
		vals := make([]string, len(cols)+1)
		vals[0] = strconv.Itoa(idx + 1)
		for i, c := range cols {
			vals[i+1] = sfapi.Field(rec, c)
		}
		fmt.Fprintln(tw, strings.Join(vals, "\t"))
	}
	return tw.Flush()
}
