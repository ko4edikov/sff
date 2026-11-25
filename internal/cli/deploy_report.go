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

// componentListMax is how many components printComponentList spells out before
// it collapses the list to per-type counts.
const componentListMax = 50

// packageItems flattens a manifest into sorted "Type:Name" strings.
func packageItems(pkg *mdapi.Package) []string {
	if pkg == nil {
		return nil
	}
	var items []string
	for _, t := range pkg.Types {
		for _, m := range t.Members {
			items = append(items, t.Name+":"+m)
		}
	}
	sort.Strings(items)
	return items
}

// printComponentList prints header followed by the components, one Type:Name per
// line (sorted). Past componentListMax it collapses to per-type counts so a big
// --changed/--commit selection stays readable. Nothing is printed for an empty
// list. destroy items, if any, are listed after under "deleting".
func printComponentList(w io.Writer, header string, items, destroy []string) {
	if len(items) == 0 && len(destroy) == 0 {
		return
	}
	fmt.Fprintln(w, header)
	printItemBlock(w, items)
	if len(destroy) > 0 {
		fmt.Fprintf(w, "deleting %d component(s):\n", len(destroy))
		printItemBlock(w, destroy)
	}
}

func printItemBlock(w io.Writer, items []string) {
	if len(items) <= componentListMax {
		for _, it := range items {
			fmt.Fprintf(w, "  %s\n", it)
		}
		return
	}
	counts := map[string]int{}
	var types []string
	for _, it := range items {
		t, _, _ := strings.Cut(it, ":")
		if counts[t] == 0 {
			types = append(types, t)
		}
		counts[t]++
	}
	sort.Strings(types)
	for _, t := range types {
		fmt.Fprintf(w, "  %-28s %d\n", t, counts[t])
	}
}
