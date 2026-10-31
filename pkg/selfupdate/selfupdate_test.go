package selfupdate

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
)

func TestCompare(t *testing.T) {
	cases := []struct {
		a, b string
		cmp  int
		ok   bool
	}{
		{"0.9.0", "v0.10.0", -1, true},
		{"v1.0.0", "1.0.0", 0, true},
		{"v1.2.0", "v1.1.9", 1, true},
		{"dev", "v1.0.0", 0, false},
		{"v0.9.0-3-gabc", "v1.0.0", 0, false},
	}
	for _, c := range cases {
		got, ok := Compare(c.a, c.b)
		if got != c.cmp || ok != c.ok {
			t.Errorf("Compare(%q,%q) = %d,%v want %d,%v", c.a, c.b, got, ok, c.cmp, c.ok)
		}
	}
}

func TestDownloadAndReplace(t *testing.T) {
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	payload := []byte("new-binary")
	tw.WriteHeader(&tar.Header{Name: "sff", Mode: 0o755, Size: int64(len(payload)), Typeflag: tar.TypeReg})
	tw.Write(payload)
	tw.Close()
	gz.Close()
	sum := sha256.Sum256(buf.Bytes())

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/o/r/releases/download/v1.2.3/sff_1.2.3_linux_amd64.tar.gz":
			w.Write(buf.Bytes())
		case "/o/r/releases/download/v1.2.3/checksums.txt":
			fmt.Fprintf(w, "%s  sff_1.2.3_linux_amd64.tar.gz\n", hex.EncodeToString(sum[:]))
		case "/repos/o/r/releases/latest":
			w.Write([]byte(`{"tag_name":"v1.2.3"}`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()

	u := New("o/r")
	u.APIBase, u.DownloadBase, u.GOOS, u.GOARCH = srv.URL, srv.URL, "linux", "amd64"
	ctx := context.Background()

	tag, err := u.LatestTag(ctx)
	if err != nil || tag != "v1.2.3" {
		t.Fatalf("LatestTag = %q, %v", tag, err)
	}
	bin, err := u.Download(ctx, tag)
	if err != nil || string(bin) != "new-binary" {
		t.Fatalf("Download = %q, %v", bin, err)
	}

	exe := filepath.Join(t.TempDir(), "sff")
	os.WriteFile(exe, []byte("old"), 0o755)
	if err := Replace(exe, bin); err != nil {
		t.Fatal(err)
	}
	if got, _ := os.ReadFile(exe); string(got) != "new-binary" {
		t.Errorf("exe = %q", got)
	}
}

func TestChecksumMismatch(t *testing.T) {
	if err := verifyChecksum([]byte("x"), "a.tar.gz", []byte("deadbeef  a.tar.gz\n")); err == nil {
		t.Error("expected mismatch error")
	}
}
