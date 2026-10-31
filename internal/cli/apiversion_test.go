package cli

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/ko4edikov/sff/pkg/auth"
	"github.com/ko4edikov/sff/pkg/mdapi"
	"github.com/ko4edikov/sff/pkg/sfapi"
)

func versionServer(t *testing.T) *auth.Org {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`[{"version":"66.0"},{"version":"68.0"}]`))
	}))
	t.Cleanup(srv.Close)
	return &auth.Org{InstanceURL: srv.URL}
}

func writeProject(t *testing.T, body string) string {
	t.Helper()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "sfdx-project.json"), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	return dir
}

func TestResolveAPIVersion(t *testing.T) {
	org := versionServer(t)
	withVersion := writeProject(t, `{"packageDirectories":[{"path":"force-app"}],"sourceApiVersion":"64.0"}`)
	withoutVersion := writeProject(t, `{"packageDirectories":[{"path":"force-app"}]}`)
	ctx := context.Background()

	cases := []struct {
		name, flag, dir, want string
	}{
		{"flag wins", "v59.0", withVersion, "v59.0"},
		{"project sourceApiVersion", "", withVersion, "v64.0"},
		{"project without version falls back to org", "", withoutVersion, "v68.0"},
		{"project lookup skipped", "", "-", "v68.0"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := resolveAPIVersion(ctx, c.flag, c.dir, org); got != c.want {
				t.Errorf("got %q, want %q", got, c.want)
			}
		})
	}

	t.Run("unreachable org falls back to default", func(t *testing.T) {
		if got := resolveAPIVersion(ctx, "", "-", &auth.Org{InstanceURL: "http://127.0.0.1:1"}); got != sfapi.DefaultAPIVersion {
			t.Errorf("got %q, want %q", got, sfapi.DefaultAPIVersion)
		}
	})
}

func TestOrgOnlyPlanRejectsBareWildcard(t *testing.T) {
	pkg := &mdapi.Package{Types: []mdapi.PackageTypes{{Name: "ApexClass", Members: []string{"*"}}}}
	if _, err := orgOnlyPlan(context.Background(), nil, pkg); err == nil {
		t.Fatal("expected an error for a bare *")
	}
}

func TestOrgOnlyPlanKeepsPlainNames(t *testing.T) {
	pkg := &mdapi.Package{Types: []mdapi.PackageTypes{{Name: "ApexClass", Members: []string{"Foo", "Bar"}}}}
	plan, err := orgOnlyPlan(context.Background(), nil, pkg) // no globs, so no org call
	if err != nil {
		t.Fatal(err)
	}
	if plan.Package != pkg || len(plan.Files) != 0 {
		t.Errorf("unexpected plan: %+v", plan)
	}
}
