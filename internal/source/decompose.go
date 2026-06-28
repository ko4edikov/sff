package source

import (
	"fmt"
	"path"
	"strings"
)

// mdNamespace is the namespace on every metadata document's root element.
const mdNamespace = "http://soap.sforce.com/2006/04/metadata"

// splitFile is one source file produced by decomposing a composed metadata file.
type splitFile struct {
	rel  string // source-relative path, e.g. objects/Account/fields/X__c.field-meta.xml
	data []byte
}

// decompose splits a composed metadata file (e.g. an .object) into its
// source-format parts: a residual parent file plus one file per decomposable
// child element. parentName is the component's developer name (the composed
// file's base name without its suffix).
//
// It relies on the Metadata API's pretty-printing — 4-space indentation with one
// element per line — which retrieve always returns; child elements therefore
// start at exactly four spaces of indentation.
func decompose(data []byte, parentName string, t *DecompType) ([]splitFile, error) {
	lines := splitLines(data)

	i := 0
	var header string
	if i < len(lines) && strings.HasPrefix(strings.TrimSpace(lines[i]), "<?xml") {
		header = lines[i]
		i++
	}
	for i < len(lines) && strings.TrimSpace(lines[i]) == "" {
		i++
	}
	if i >= len(lines) {
		return nil, fmt.Errorf("%s: empty metadata document", parentName)
	}
	rootOpen := lines[i]
	i++

	parentDir := path.Join(t.DirectoryName, parentName)
	var residual []string
	var children []splitFile

	for i < len(lines) {
		line := lines[i]
		trimmed := strings.TrimSpace(line)
		if trimmed == "</"+t.Name+">" {
			break
		}
		if trimmed == "" {
			i++
			continue
		}
		if indentOf(line) == 4 && strings.HasPrefix(trimmed, "<") && !strings.HasPrefix(trimmed, "</") {
			tag := elementName(trimmed)
			block, next := captureElement(lines, i)
			i = next
			if child, ok := t.childByTag(tag); ok {
				sf, err := buildChildFile(block, child, t, parentDir, header)
				if err != nil {
					return nil, fmt.Errorf("%s/%s: %w", parentName, tag, err)
				}
				children = append(children, sf)
			} else {
				residual = append(residual, block...)
			}
			continue
		}
		// Unexpected top-level content — keep it in the parent verbatim.
		residual = append(residual, line)
		i++
	}

	var b strings.Builder
	if header != "" {
		b.WriteString(header)
		b.WriteByte('\n')
	}
	b.WriteString(rootOpen)
	b.WriteByte('\n')
	for _, l := range residual {
		b.WriteString(l)
		b.WriteByte('\n')
	}
	b.WriteString("</")
	b.WriteString(t.Name)
	b.WriteString(">\n")

	parentRel := path.Join(parentDir, parentName+"."+t.Suffix+"-meta.xml")
	out := []splitFile{{rel: parentRel, data: []byte(b.String())}}
	return append(out, children...), nil
}

// buildChildFile renders one decomposed child as its own source file: the block
// is de-indented by one level, its outer tag is rewritten to the child type's
// root element, and the metadata namespace is added.
func buildChildFile(block []string, child DecompChild, t *DecompType, parentDir, header string) (splitFile, error) {
	fullName := extractFullName(block)
	if fullName == "" {
		return splitFile{}, fmt.Errorf("no <fullName> in child element")
	}

	dedented := make([]string, len(block))
	for i, l := range block {
		dedented[i] = strings.TrimPrefix(l, "    ")
	}

	// Rewrite the opening tag (line 0) and the closing tag (last line) to the
	// child type's root element. The common case is a multi-line block whose
	// first line is "<tag>" and last line "</tag>".
	open := dedented[0]
	selfClose := strings.HasSuffix(strings.TrimSpace(open), "/>")
	switch {
	case selfClose:
		dedented[0] = "<" + child.Type + ` xmlns="` + mdNamespace + `"/>`
	default:
		dedented[0] = "<" + child.Type + ` xmlns="` + mdNamespace + `">`
		last := len(dedented) - 1
		dedented[last] = "</" + child.Type + ">"
	}

	var b strings.Builder
	if header != "" {
		b.WriteString(header)
		b.WriteByte('\n')
	}
	for _, l := range dedented {
		b.WriteString(l)
		b.WriteByte('\n')
	}

	dir := parentDir
	if t.Layout == "folderPerType" {
		dir = path.Join(parentDir, child.XMLTag)
	}
	rel := path.Join(dir, fullName+"."+child.Suffix+"-meta.xml")
	return splitFile{rel: rel, data: []byte(b.String())}, nil
}

// captureElement returns the lines spanning a top-level element starting at
// index start, and the index just past it. A single-line element (self-closing
// or with an inline close) spans one line; otherwise it runs until the matching
// "</tag>" at the same four-space indentation.
func captureElement(lines []string, start int) ([]string, int) {
	first := lines[start]
	tag := elementName(strings.TrimSpace(first))
	trimmed := strings.TrimSpace(first)
	if strings.HasSuffix(trimmed, "/>") || strings.Contains(trimmed, "</"+tag+">") {
		return lines[start : start+1], start + 1
	}
	close := "</" + tag + ">"
	for i := start + 1; i < len(lines); i++ {
		if strings.TrimSpace(lines[i]) == close && indentOf(lines[i]) == 4 {
			return lines[start : i+1], i + 1
		}
	}
	return lines[start:], len(lines) // malformed; take the rest
}

// extractFullName returns the value of the first <fullName>…</fullName> in block.
func extractFullName(block []string) string {
	for _, l := range block {
		t := strings.TrimSpace(l)
		const open, close = "<fullName>", "</fullName>"
		if strings.HasPrefix(t, open) && strings.HasSuffix(t, close) {
			return t[len(open) : len(t)-len(close)]
		}
	}
	return ""
}

// elementName extracts the tag name from a trimmed opening tag like "<fields>",
// "<fields attr='x'>", or "<fields/>".
func elementName(trimmed string) string {
	s := strings.TrimPrefix(trimmed, "<")
	end := strings.IndexAny(s, " \t>/")
	if end < 0 {
		return s
	}
	return s[:end]
}

// indentOf counts leading space characters.
func indentOf(line string) int {
	n := 0
	for n < len(line) && line[n] == ' ' {
		n++
	}
	return n
}

// splitLines splits on "\n" and drops a single trailing empty line from a final
// newline, so re-joining with "\n"+terminator reproduces the original.
func splitLines(data []byte) []string {
	lines := strings.Split(string(data), "\n")
	if n := len(lines); n > 0 && lines[n-1] == "" {
		lines = lines[:n-1]
	}
	return lines
}
