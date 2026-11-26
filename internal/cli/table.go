package cli

import (
	"fmt"
	"io"
	"os"
	"strings"
	"unicode/utf8"

	"golang.org/x/term"
)

// termWidth returns the stdout terminal width, or 0 when stdout is not a TTY
// (no wrapping is applied then, so piped output stays one row per record).
func termWidth() int {
	if w, _, err := term.GetSize(int(os.Stdout.Fd())); err == nil && w > 0 {
		return w
	}
	return 0
}

// printTable renders an sf-style table: an optional "=== title" line, a header
// row, a rule of ─ under every column, a blank line, then the rows. Cells may
// hold several lines; when maxWidth > 0 and the table is too wide, the widest
// columns are shrunk and their cells wrapped.
func printTable(w io.Writer, title string, headers []string, rows [][]string, maxWidth int) {
	const gap = 3
	if title != "" {
		fmt.Fprintf(w, "=== %s\n", title)
	}
	widths := make([]int, len(headers))
	for i, h := range headers {
		widths[i] = utf8.RuneCountInString(h)
	}
	for _, r := range rows {
		for i, c := range r {
			for _, ln := range strings.Split(c, "\n") {
				if n := utf8.RuneCountInString(ln); n > widths[i] {
					widths[i] = n
				}
			}
		}
	}
	if maxWidth > 0 {
		for {
			total := gap * (len(widths) - 1)
			widest := 0
			for i, n := range widths {
				total += n
				if n > widths[widest] {
					widest = i
				}
			}
			over := total - maxWidth
			if over <= 0 || widths[widest] <= 20 {
				break
			}
			cut := over
			if widths[widest]-cut < 20 {
				cut = widths[widest] - 20
			}
			widths[widest] -= cut
		}
	}

	line := func(cells []string) {
		var b strings.Builder
		for i, c := range cells {
			if i > 0 {
				b.WriteString(strings.Repeat(" ", gap))
			}
			b.WriteString(c)
			if i < len(cells)-1 {
				if pad := widths[i] - utf8.RuneCountInString(c); pad > 0 {
					b.WriteString(strings.Repeat(" ", pad))
				}
			}
		}
		fmt.Fprintln(w, strings.TrimRight(b.String(), " "))
	}
	line(headers)
	rule := make([]string, len(headers))
	for i, n := range widths {
		rule[i] = strings.Repeat("─", n)
	}
	line(rule)
	fmt.Fprintln(w)
	for _, r := range rows {
		wrapped := make([][]string, len(headers))
		height := 1
		for i := range headers {
			cell := ""
			if i < len(r) {
				cell = r[i]
			}
			wrapped[i] = wrapCell(cell, widths[i])
			if len(wrapped[i]) > height {
				height = len(wrapped[i])
			}
		}
		for k := 0; k < height; k++ {
			cells := make([]string, len(headers))
			for i := range headers {
				if k < len(wrapped[i]) {
					cells[i] = wrapped[i][k]
				}
			}
			line(cells)
		}
	}
}

// wrapCell splits s on newlines and hard-wraps each line to width runes.
func wrapCell(s string, width int) []string {
	var out []string
	for _, ln := range strings.Split(strings.TrimRight(s, "\n"), "\n") {
		ln = strings.TrimRight(ln, " \r\t")
		r := []rune(ln)
		if width <= 0 || len(r) <= width {
			out = append(out, ln)
			continue
		}
		for len(r) > width {
			out = append(out, string(r[:width]))
			r = r[width:]
		}
		out = append(out, string(r))
	}
	return out
}
