package sfapi

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/ko4edikov/sff/pkg/auth"
)

func TestLatestAPIVersion(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/services/data/" {
			t.Errorf("path = %q", r.URL.Path)
		}
		w.Write([]byte(`[{"version":"9.0"},{"version":"64.0"},{"version":"100.0"},{"version":"68.0"}]`))
	}))
	defer srv.Close()

	got, err := LatestAPIVersion(context.Background(), &auth.Org{InstanceURL: srv.URL + "/"})
	if err != nil {
		t.Fatal(err)
	}
	if got != "v100.0" {
		t.Errorf("got %q, want v100.0 (numeric, not lexical, max)", got)
	}
}

func TestLatestAPIVersionErrors(t *testing.T) {
	for name, handler := range map[string]http.HandlerFunc{
		"status": func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusInternalServerError) },
		"empty":  func(w http.ResponseWriter, r *http.Request) { w.Write([]byte(`[]`)) },
		"junk":   func(w http.ResponseWriter, r *http.Request) { w.Write([]byte(`not json`)) },
	} {
		t.Run(name, func(t *testing.T) {
			srv := httptest.NewServer(handler)
			defer srv.Close()
			if _, err := LatestAPIVersion(context.Background(), &auth.Org{InstanceURL: srv.URL}); err == nil {
				t.Error("expected an error")
			}
		})
	}
}
