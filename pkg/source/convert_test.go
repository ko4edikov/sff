package source

import (
	"archive/zip"
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ko4edikov/sff/pkg/project"
)

// buildRetrieveZip builds an in-memory metadata-format retrieve zip (like the
// one the Metadata API returns) from a set of entry name → content pairs.
func buildRetrieveZip(t *testing.T, entries map[string]string) []byte {
	t.Helper()
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	for name, content := range entries {
		w, err := zw.Create(name)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := w.Write([]byte(content)); err != nil {
			t.Fatal(err)
		}
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

// TestConvertZipToSourceNormalizesCRLFInVerbatimContent verifies that a
// verbatim content type (ApexClass) whose org-returned body uses CRLF line
// endings is written with LF, matching an LF-committed project instead of
// churning every line on every retrieve.
func TestConvertZipToSourceNormalizesCRLFInVerbatimContent(t *testing.T) {
	root := t.TempDir()
	proj := &project.Project{Root: root, Dirs: []project.Dir{{Path: "force-app", Default: true}}}

	zipBytes := buildRetrieveZip(t, map[string]string{
		"classes/Foo.cls":          "public class Foo {\r\n\tpublic void bar() {}\r\n}\r\n",
		"classes/Foo.cls-meta.xml": "<ApexClass xmlns=\"http://soap.sforce.com/2006/04/metadata\">\r\n<status>Active</status>\r\n</ApexClass>",
	})

	if _, err := ConvertZipToSource(zipBytes, proj, nil, "", nil); err != nil {
		t.Fatal(err)
	}

	clsPath := filepath.Join(root, "force-app", "classes", "Foo.cls")
	data, err := os.ReadFile(clsPath)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(data), "\r") {
		t.Fatalf("Foo.cls still contains CR bytes: %q", data)
	}
	want := "public class Foo {\n\tpublic void bar() {}\n}\n"
	if string(data) != want {
		t.Fatalf("Foo.cls = %q, want %q", data, want)
	}

	// The -meta.xml sidecar sits in the same verbatim (content-type) folder, so
	// it gets the same CRLF→LF treatment as the .cls file.
	metaPath := clsPath + "-meta.xml"
	meta, err := os.ReadFile(metaPath)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(meta), "\r") {
		t.Fatalf("Foo.cls-meta.xml still contains CR bytes: %q", meta)
	}
}
