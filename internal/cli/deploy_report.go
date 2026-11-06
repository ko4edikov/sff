package cli

import (
	"fmt"
	"io"
	"sort"
	"strings"
	"text/tabwriter"

	"github.com/ko4edikov/sff/pkg/mdapi"
)

// deployRow is one line of the post-deploy table.
type deployRow struct {
	Type, Name, State string
}

// deployState maps a component result to the state sf shows.
func deployState(c mdapi.ComponentMessage) string {
	switch {
	case c.Deleted:
		return "Deleted"
	case c.Created:
		return "Created"
	case c.Changed:
		return "Changed"
	}
	return "Unchanged"
}

// printDeploySuccesses prints the deployed components grouped by metadata type
// (type, then name) with their per-component result. Deletions from
// destructiveChanges appear in the same table with state Deleted. The
// package.xml pseudo-component the API echoes back is omitted.
func printDeploySuccesses(w io.Writer, comps []mdapi.ComponentMessage) {
	var rows []deployRow
	for _, c := range comps {
		if c.FullName == "package.xml" || c.ComponentType == "" {
			continue
		}
		rows = append(rows, deployRow{c.ComponentType, c.FullName, deployState(c)})
	}
	printDeployRows(w, rows)
}

// printToolingSuccesses is the Tooling API equivalent; it only updates
// existing components, so every row is Changed.
func printToolingSuccesses(w io.Writer, succeeded []string) {
	var rows []deployRow
	for _, s := range succeeded {
		t, n, ok := strings.Cut(s, ":")
		if !ok {
			continue
		}
		rows = append(rows, deployRow{t, n, "Changed"})
	}
	printDeployRows(w, rows)
}

func printDeployRows(w io.Writer, rows []deployRow) {
	if len(rows) == 0 {
		return
	}
	sort.Slice(rows, func(i, j int) bool {
		if rows[i].Type != rows[j].Type {
			return rows[i].Type < rows[j].Type
		}
		return rows[i].Name < rows[j].Name
	})
	fmt.Fprintln(w)
	tw := tabwriter.NewWriter(w, 0, 2, 2, ' ', 0)
	fmt.Fprintln(tw, "TYPE\tNAME\tSTATE")
	for _, r := range rows {
		fmt.Fprintf(tw, "%s\t%s\t%s\n", r.Type, r.Name, r.State)
	}
	tw.Flush()
}
