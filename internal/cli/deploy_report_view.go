package cli

import (
	"fmt"
	"io"
	"os"
	"sort"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/ko4edikov/sff/pkg/auth"
	"github.com/ko4edikov/sff/pkg/mdapi"
)

// reportStyle colors report output when w is a terminal (and NO_COLOR is unset).
type reportStyle struct{ on bool }

func newReportStyle(w io.Writer) reportStyle {
	return reportStyle{colorSupported && isTerminal(w) && os.Getenv("NO_COLOR") == ""}
}

func (s reportStyle) wrap(code, text string) string {
	if !s.on {
		return text
	}
	return "\x1b[" + code + "m" + text + "\x1b[0m"
}

func (s reportStyle) heading(t string) string { return s.wrap("1;34", t) }
func (s reportStyle) bad(t string) string     { return s.wrap("1;31", t) }
func (s reportStyle) dim(t string) string     { return s.wrap("2", t) }
func (s reportStyle) under(t string) string   { return s.wrap("4", t) }

func (s reportStyle) status(t string) string {
	switch t {
	case "Succeeded":
		return s.wrap("1;32", t)
	case "Failed", "Canceled", "Canceling":
		return s.wrap("1;31", t)
	}
	return s.wrap("1;33", t)
}

// printBoxTable renders an sf-style boxed table under a heading. Cells must not
// contain newlines or escapes (padding counts runes).
func printBoxTable(w io.Writer, st reportStyle, title string, headers []string, rows [][]string) {
	widths := make([]int, len(headers))
	for i, h := range headers {
		widths[i] = utf8.RuneCountInString(h)
	}
	for _, r := range rows {
		for i, c := range r {
			if n := utf8.RuneCountInString(c); n > widths[i] {
				widths[i] = n
			}
		}
	}
	rule := func(l, m, r string) {
		parts := make([]string, len(widths))
		for i, n := range widths {
			parts[i] = strings.Repeat("─", n+2)
		}
		fmt.Fprintln(w, l+strings.Join(parts, m)+r)
	}
	row := func(cells []string) {
		var b strings.Builder
		b.WriteString("│")
		for i, n := range widths {
			c := ""
			if i < len(cells) {
				c = cells[i]
			}
			b.WriteString(" " + c + strings.Repeat(" ", n-utf8.RuneCountInString(c)) + " │")
		}
		fmt.Fprintln(w, b.String())
	}
	fmt.Fprintf(w, "\n%s\n", st.heading(title))
	rule("┌", "┬", "┐")
	row(headers)
	rule("├", "┼", "┤")
	for _, r := range rows {
		row(r)
	}
	rule("└", "┴", "┘")
}

// printDeployReport prints a finished (or running) deploy in the layout of
// `sf project deploy report`: Deploy Info, Deploy Options, Deployed/Deleted
// Source, Component Failures, Test Failures and a Test Results Summary. With
// withTests it appends the passing tests and per-class code coverage.
func printDeployReport(w io.Writer, res *mdapi.DeployResult, org *auth.Org, withTests bool) {
	st := newReportStyle(w)
	fmt.Fprintf(w, "%s... %s\n", res.ID, st.status(res.Status))
	if res.ErrorMessage != "" {
		fmt.Fprintf(w, "\n%s %s\n", st.bad("Error:"), res.ErrorMessage)
	}

	b := func(v bool) string { return strconv.FormatBool(v) }
	n := strconv.Itoa
	info := [][]string{
		{"checkOnly", b(res.CheckOnly)},
		{"completedDate", res.CompletedDate},
		{"createdBy", res.CreatedBy},
		{"createdByName", res.CreatedByName},
		{"createdDate", res.CreatedDate},
		{"done", b(res.Done)},
		{"id", res.ID},
		{"ignoreWarnings", b(res.IgnoreWarnings)},
		{"lastModifiedDate", res.LastModifiedDate},
		{"numberComponentErrors", n(res.NumberComponentErrors)},
		{"numberComponentsDeployed", n(res.NumberComponentsDeployed)},
		{"numberComponentsTotal", n(res.NumberComponentsTotal)},
		{"numberTestErrors", n(res.NumberTestErrors)},
		{"numberTestsCompleted", n(res.NumberTestsCompleted)},
		{"numberTestsTotal", n(res.NumberTestsTotal)},
		{"rollbackOnError", b(res.RollbackOnError)},
		{"runTestsEnabled", b(res.RunTestsEnabled)},
		{"startDate", res.StartDate},
		{"status", res.Status},
		{"success", b(res.Success)},
	}
	printBoxTable(w, st, "Deploy Info", []string{"Key", "Value"}, info)
	printBoxTable(w, st, "Deploy Options", []string{"Key", "Value"},
		[][]string{{"target-org", org.Username}, {"job-id", res.ID}})

	// Deployed/Deleted Source only mean something when the deploy was applied;
	// a failed deploy is rolled back although the API still lists its components.
	if res.Success || res.Status == "SucceededPartial" {
		var deployed, deleted [][]string
		for _, c := range res.ComponentSuccesses {
			if c.FullName == "package.xml" || c.ComponentType == "" {
				continue
			}
			row := []string{deployState(c), c.FullName, c.ComponentType}
			if c.Deleted {
				deleted = append(deleted, row)
			} else {
				deployed = append(deployed, row)
			}
		}
		sortRows := func(rows [][]string) {
			sort.Slice(rows, func(i, j int) bool {
				if rows[i][2] != rows[j][2] {
					return rows[i][2] < rows[j][2]
				}
				return rows[i][1] < rows[j][1]
			})
		}
		sortRows(deployed)
		sortRows(deleted)
		if len(deployed) > 0 {
			printBoxTable(w, st, "Deployed Source", []string{"State", "Name", "Type"}, deployed)
		}
		if len(deleted) > 0 {
			printBoxTable(w, st, "Deleted Source", []string{"State", "Name", "Type"}, deleted)
		}
	}

	if len(res.ComponentFailures) > 0 {
		fails := append([]mdapi.ComponentMessage(nil), res.ComponentFailures...)
		sort.Slice(fails, func(i, j int) bool { return fails[i].FullName < fails[j].FullName })
		fmt.Fprintf(w, "\n%s\n", st.bad(fmt.Sprintf("Component Failures [%d]", len(fails))))
		for _, f := range fails {
			loc := f.FullName
			if f.ComponentType != "" {
				loc = f.ComponentType + ": " + f.FullName
			}
			fmt.Fprintf(w, "• %s\n", st.under(loc))
			if f.LineNumber > 0 {
				fmt.Fprintf(w, "  %s: line %d, column %d\n", st.dim("location"), f.LineNumber, f.ColumnNumber)
			}
			if f.ProblemType != "" {
				fmt.Fprintf(w, "  %s: %s\n", st.dim("type"), f.ProblemType)
			}
			fmt.Fprintf(w, "  %s: %s\n\n", st.dim("problem"), f.Problem)
		}
	}

	if len(res.TestFailures) > 0 {
		fmt.Fprintf(w, "\n%s\n", st.bad(fmt.Sprintf("Test Failures [%d]", len(res.TestFailures))))
		for _, t := range res.TestFailures {
			fmt.Fprintf(w, "• %s\n", st.under(t.Name+"."+t.MethodName))
			fmt.Fprintf(w, "  %s: %s\n", st.dim("message"), t.Message)
			if tr := strings.TrimSpace(t.StackTrace); tr != "" {
				fmt.Fprintf(w, "  %s:\n", st.dim("stacktrace"))
				for _, ln := range strings.Split(tr, "\n") {
					fmt.Fprintf(w, "    %s\n", strings.TrimSpace(ln))
				}
			}
			fmt.Fprintln(w)
		}
	}

	if res.NumberTestsTotal > 0 {
		fmt.Fprintf(w, "\n%s\n", st.heading("Test Results Summary"))
		fmt.Fprintf(w, "Passing: %d\nFailing: %d\nTotal: %d\n", res.NumberTestsCompleted-res.NumberTestErrors, res.NumberTestErrors, res.NumberTestsTotal)
	}
	if withTests {
		printDeployTests(w, res)
	}
}
