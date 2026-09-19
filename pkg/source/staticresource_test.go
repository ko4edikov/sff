package source

import (
	"archive/zip"
	"bytes"
	"strings"
	"testing"
)

// buildZip builds an in-memory zip archive from name → content pairs.
func buildZip(t *testing.T, entries map[string][]byte) []byte {
	t.Helper()
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	for name, content := range entries {
		w, err := zw.Create(name)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := w.Write(content); err != nil {
			t.Fatal(err)
		}
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

// TestUnzipStaticResourceNormalizesTextButNotBinary verifies that a text file
// inside an expanded static resource archive (e.g. a vendored JS library
// shipped as a zip) gets CRLF→LF normalized, while a binary entry whose bytes
// happen to contain a CRLF sequence is left byte-for-byte, since it isn't a
// known text extension.
func TestUnzipStaticResourceNormalizesTextButNotBinary(t *testing.T) {
	binary := []byte{0x89, 'P', 'N', 'G', '\r', '\n', 0x1a, 0x0a, 0x00, 0xff}
	archive := buildZip(t, map[string][]byte{
		"scripts.js": []byte("function f() {\r\n\treturn 1;\r\n}\r\n"),
		"icon.png":   binary,
	})

	out, err := unzipStaticResource("staticresources/Foo", archive)
	if err != nil {
		t.Fatal(err)
	}

	byRel := make(map[string][]byte, len(out))
	for _, f := range out {
		byRel[f.rel] = f.data
	}

	js, ok := byRel["staticresources/Foo/scripts.js"]
	if !ok {
		t.Fatalf("scripts.js missing from output: %v", byRel)
	}
	if strings.Contains(string(js), "\r") {
		t.Fatalf("scripts.js still contains CR bytes: %q", js)
	}
	if string(js) != "function f() {\n\treturn 1;\n}\n" {
		t.Fatalf("scripts.js = %q, want normalized content", js)
	}

	png, ok := byRel["staticresources/Foo/icon.png"]
	if !ok {
		t.Fatalf("icon.png missing from output: %v", byRel)
	}
	if !bytes.Equal(png, binary) {
		t.Fatalf("icon.png was modified: got %v, want %v", png, binary)
	}
}
