package source

import (
	"bytes"
	"regexp"
)

var trailingWS = regexp.MustCompile(`[ \t\r\n]+$`)

// Normalize cleans org-side content so cosmetic differences don't show up as
// diffs (mirrors sf-compare's finalize_file): CRLF→LF and trailing whitespace
// at end-of-file are stripped. When local is non-nil and ends in a newline, the
// result is given exactly one trailing newline to match. local may be nil
// (org-only file), in which case no final-newline adjustment is made.
func Normalize(remote, local []byte) []byte {
	out := bytes.ReplaceAll(remote, []byte("\r\n"), []byte("\n"))
	out = bytes.ReplaceAll(out, []byte("\r"), []byte("\n"))
	out = trailingWS.ReplaceAll(out, nil)

	if local != nil && bytes.HasSuffix(local, []byte("\n")) {
		out = append(out, '\n')
	}
	return out
}
