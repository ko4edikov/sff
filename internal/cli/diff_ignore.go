package cli

import (
	"fmt"
	"strings"
	"unicode"

	"github.com/pmezard/go-difflib/difflib"
)

// ignoreMode controls how the built-in unified diff treats whitespace, mirroring
// IntelliJ's "Do not ignore / Trim / Ignore whitespaces / …" menu. It affects
// only the in-process diff; an external viewer applies its own setting.
type ignoreMode int

const (
	ignoreNone    ignoreMode = iota // compare lines verbatim
	ignoreTrim                      // ignore leading/trailing whitespace per line
	ignoreWS                        // ignore all whitespace within a line
	ignoreWSBlank                   // ignore all whitespace and blank lines
)

// parseIgnore maps a --ignore flag value (and a few aliases) to a mode.
func parseIgnore(s string) (ignoreMode, error) {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "", "none":
		return ignoreNone, nil
	case "trim":
		return ignoreTrim, nil
	case "whitespace", "ws", "space":
		return ignoreWS, nil
	case "whitespace-blank", "ws-blank", "space-blank":
		return ignoreWSBlank, nil
	default:
		return ignoreNone, fmt.Errorf("invalid --ignore %q (want none, trim, whitespace, or whitespace-blank)", s)
	}
}

// ignoreKey returns the comparison key for a line under mode m and whether the
// line is kept at all (blank lines are dropped in ignoreWSBlank). The trailing
// newline is stripped for the key; display still uses the original line.
func ignoreKey(line string, m ignoreMode) (string, bool) {
	s := strings.TrimSuffix(line, "\n")
	switch m {
	case ignoreTrim:
		s = strings.TrimSpace(s)
	case ignoreWS, ignoreWSBlank:
		s = stripSpace(s)
	}
	if m == ignoreWSBlank && s == "" {
		return s, false
	}
	return s, true
}

// stripSpace removes every Unicode whitespace rune from s.
func stripSpace(s string) string {
	var b strings.Builder
	b.Grow(len(s))
	for _, r := range s {
		if !unicode.IsSpace(r) {
			b.WriteRune(r)
		}
	}
	return b.String()
}

// prepLines splits lines into parallel original/key slices under mode m: orig is
// shown in the diff, key drives the comparison. Lines dropped by the mode are
// removed from both, keeping the slices index-aligned.
func prepLines(lines []string, m ignoreMode) (orig, key []string) {
	for _, l := range lines {
		k, keep := ignoreKey(l, m)
		if !keep {
			continue
		}
		orig = append(orig, l)
		key = append(key, k)
	}
	return orig, key
}

// unifiedDiff renders a unified diff of two files: matching is done on the key
// lines (so ignored whitespace doesn't register as a change) while the original
// lines are displayed. It returns "" when the keys are identical.
func unifiedDiff(aOrig, bOrig, aKey, bKey []string, fromFile, fromDate, toFile, toDate string, ctx int) string {
	m := difflib.NewMatcher(aKey, bKey)
	groups := m.GetGroupedOpCodes(ctx)
	if len(groups) == 0 {
		return ""
	}
	var buf strings.Builder
	fmt.Fprintf(&buf, "--- %s\t%s\n", fromFile, fromDate)
	fmt.Fprintf(&buf, "+++ %s\t%s\n", toFile, toDate)
	for _, group := range groups {
		first, last := group[0], group[len(group)-1]
		fmt.Fprintf(&buf, "@@ -%s +%s @@\n",
			formatRange(first.I1, last.I2), formatRange(first.J1, last.J2))
		for _, c := range group {
			switch c.Tag {
			case 'e': // equal
				for _, line := range aOrig[c.I1:c.I2] {
					writeLine(&buf, " ", line)
				}
			case 'd': // delete
				for _, line := range aOrig[c.I1:c.I2] {
					writeLine(&buf, "-", line)
				}
			case 'i': // insert
				for _, line := range bOrig[c.J1:c.J2] {
					writeLine(&buf, "+", line)
				}
			case 'r': // replace
				for _, line := range aOrig[c.I1:c.I2] {
					writeLine(&buf, "-", line)
				}
				for _, line := range bOrig[c.J1:c.J2] {
					writeLine(&buf, "+", line)
				}
			}
		}
	}
	return buf.String()
}

// writeLine writes a diff line with its prefix, ensuring a trailing newline.
func writeLine(buf *strings.Builder, prefix, line string) {
	buf.WriteString(prefix)
	buf.WriteString(line)
	if !strings.HasSuffix(line, "\n") {
		buf.WriteByte('\n')
	}
}

// formatRange renders a unified-diff hunk range (1-based; an empty range begins
// at the line before it), matching go-difflib's own formatting.
func formatRange(start, stop int) string {
	length := stop - start
	if length == 1 {
		return fmt.Sprintf("%d", start+1)
	}
	beginning := start + 1
	if length == 0 {
		beginning = start
	}
	return fmt.Sprintf("%d,%d", beginning, length)
}
