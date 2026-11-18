package cli

import (
	"errors"
	"fmt"
	"os"
	"strings"

	"golang.org/x/term"
)

// errPickCancelled is returned by pickRow when the user quits without choosing.
var errPickCancelled = errors.New("cancelled")

// pickRow shows rows in an interactive list on stderr and returns the index of
// the chosen one. Keys: ↑/↓ (j/k), PgUp/PgDn, Home/End move; "/" filters
// (Enter keeps the filter, Esc clears it); Enter selects; q, Esc or Ctrl-C
// cancels. header is printed, undimmed, above the rows. It requires a TTY on
// stdin and stderr.
func pickRow(title, header string, rows []string) (int, error) {
	in, out := int(os.Stdin.Fd()), int(os.Stderr.Fd())
	if !term.IsTerminal(in) || !term.IsTerminal(out) {
		return -1, errors.New("interactive selection needs a terminal")
	}
	if len(rows) == 0 {
		return -1, errors.New("nothing to choose from")
	}
	old, err := term.MakeRaw(in)
	if err != nil {
		return -1, err
	}
	defer term.Restore(in, old)
	fmt.Fprint(os.Stderr, "\x1b[?25l") // hide cursor
	defer fmt.Fprint(os.Stderr, "\x1b[?25h")

	var (
		filter    string
		filtering bool
		cursor    int // index into visible
		top       int // first visible row on screen
		drawn     int // lines printed by the previous frame
	)
	visible := func() []int {
		var v []int
		f := strings.ToLower(filter)
		for i, r := range rows {
			if f == "" || strings.Contains(strings.ToLower(r), f) {
				v = append(v, i)
			}
		}
		return v
	}
	clear := func() {
		if drawn > 0 {
			fmt.Fprintf(os.Stderr, "\x1b[%dA\r\x1b[J", drawn)
			drawn = 0
		}
	}
	draw := func(vis []int) {
		clear()
		w, h, err := term.GetSize(out)
		if err != nil || w < 20 || h < 8 {
			w, h = 100, 24
		}
		page := h - 5
		if page > len(vis) {
			page = len(vis)
		}
		if cursor >= len(vis) {
			cursor = len(vis) - 1
		}
		if cursor < 0 {
			cursor = 0
		}
		if cursor < top {
			top = cursor
		}
		if page > 0 && cursor >= top+page {
			top = cursor - page + 1
		}
		fit := func(s string) string {
			if r := []rune(s); len(r) > w-1 {
				return string(r[:w-2]) + "…"
			}
			return s
		}
		line := func(s string) { fmt.Fprint(os.Stderr, s, "\r\n"); drawn++ }
		line(fit(title))
		if header != "" {
			line("\x1b[2m" + fit("  "+header) + "\x1b[0m")
		}
		for i := top; i < top+page; i++ {
			s := fit(rows[vis[i]])
			if i == cursor {
				line("\x1b[7m" + fit("▸ "+rows[vis[i]]) + "\x1b[0m")
			} else {
				line("  " + s)
			}
		}
		if len(vis) == 0 {
			line("  (no matches)")
		}
		switch {
		case filtering:
			line("filter: " + filter + "█   (Enter keep · Esc clear)")
		case filter != "":
			line(fmt.Sprintf("filter: %s   (%d/%d)  / edit · Esc clear · Enter select · q quit", filter, len(vis), len(rows)))
		default:
			line("↑/↓ move · Enter select · / filter · q quit")
		}
	}

	buf := make([]byte, 16)
	for {
		vis := visible()
		draw(vis)
		n, err := os.Stdin.Read(buf)
		if err != nil {
			clear()
			return -1, err
		}
		k := string(buf[:n])
		page := 10
		switch {
		case filtering:
			switch {
			case k == "\r" || k == "\n":
				filtering = false
			case k == "\x1b":
				filtering, filter = false, ""
			case k == "\x03":
				clear()
				return -1, errPickCancelled
			case k == "\x7f" || k == "\b":
				if r := []rune(filter); len(r) > 0 {
					filter = string(r[:len(r)-1])
				}
				cursor, top = 0, 0
			case n == 1 && buf[0] >= 0x20:
				filter += k
				cursor, top = 0, 0
			}
		case k == "\x1b[A" || k == "k":
			cursor--
		case k == "\x1b[B" || k == "j":
			cursor++
		case k == "\x1b[5~":
			cursor -= page
		case k == "\x1b[6~":
			cursor += page
		case k == "\x1b[H" || k == "g":
			cursor = 0
		case k == "\x1b[F" || k == "G":
			cursor = len(vis) - 1
		case k == "/":
			filtering = true
		case k == "\r" || k == "\n":
			if len(vis) > 0 {
				clear()
				return vis[cursor], nil
			}
		case k == "\x1b" && filter != "":
			filter = ""
			cursor, top = 0, 0
		case k == "q" || k == "\x1b" || k == "\x03":
			clear()
			return -1, errPickCancelled
		}
		if cursor < 0 {
			cursor = 0
		}
		if cursor > len(vis)-1 && len(vis) > 0 {
			cursor = len(vis) - 1
		}
	}
}
