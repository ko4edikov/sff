package cli

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ko4edikov/sff/internal/mdapi"
)

func TestResolveTestLevel(t *testing.T) {
	tests := []struct {
		name    string
		level   string
		tests   []string
		want    string
		wantErr string
	}{
		{name: "empty stays empty", level: "", want: ""},
		{name: "none", level: "NoTestRun", want: mdapi.TestLevelNone},
		{name: "local", level: "RunLocalTests", want: mdapi.TestLevelLocal},
		{name: "all", level: "RunAllTestsInOrg", want: mdapi.TestLevelAll},
		{name: "case-insensitive", level: "runlocaltests", want: mdapi.TestLevelLocal},
		{name: "trims whitespace", level: "  RunLocalTests  ", want: mdapi.TestLevelLocal},
		{name: "specified with tests", level: "RunSpecifiedTests", tests: []string{"MyTest"}, want: mdapi.TestLevelSpecified},
		{name: "invalid level", level: "bogus", wantErr: "invalid --test-level"},
		{name: "tests without level", tests: []string{"MyTest"}, wantErr: "--tests requires --test-level RunSpecifiedTests"},
		{name: "tests with wrong level", level: "RunLocalTests", tests: []string{"MyTest"}, wantErr: "--tests requires --test-level RunSpecifiedTests"},
		{name: "specified without tests", level: "RunSpecifiedTests", wantErr: "requires at least one --tests"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := resolveTestLevel(tt.level, tt.tests)
			if tt.wantErr != "" {
				if err == nil {
					t.Fatalf("want error containing %q, got nil (result %q)", tt.wantErr, got)
				}
				if !strings.Contains(err.Error(), tt.wantErr) {
					t.Fatalf("error = %q, want containing %q", err, tt.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("resolveTestLevel: %v", err)
			}
			if got != tt.want {
				t.Errorf("level = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestComponentCount(t *testing.T) {
	tests := []struct {
		name string
		pkg  *mdapi.Package
		want int
	}{
		{name: "empty", pkg: &mdapi.Package{}, want: 0},
		{
			name: "single type",
			pkg:  &mdapi.Package{Types: []mdapi.PackageTypes{{Name: "ApexClass", Members: []string{"A", "B"}}}},
			want: 2,
		},
		{
			name: "multiple types",
			pkg: &mdapi.Package{Types: []mdapi.PackageTypes{
				{Name: "ApexClass", Members: []string{"A", "B"}},
				{Name: "LightningComponentBundle", Members: []string{"c"}},
			}},
			want: 3,
		},
		{
			name: "wildcard member",
			pkg:  &mdapi.Package{Types: []mdapi.PackageTypes{{Name: "ApexClass", Members: []string{"*"}}}},
			want: 1,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := componentCount(tt.pkg); got != tt.want {
				t.Errorf("componentCount = %d, want %d", got, tt.want)
			}
		})
	}
}

func TestResolveToolingComponents(t *testing.T) {
	t.Run("source dir routes apex + static, skips the rest", func(t *testing.T) {
		dir := t.TempDir()
		write := func(rel, body string) {
			full := filepath.Join(dir, filepath.FromSlash(rel))
			if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(full, []byte(body), 0o644); err != nil {
				t.Fatal(err)
			}
		}
		write("classes/Foo.cls", "public class Foo {}")
		write("staticresources/Baz.txt", "hello")
		write("staticresources/Baz.resource-meta.xml",
			`<?xml version="1.0"?><StaticResource><contentType>text/plain</contentType><cacheControl>Public</cacheControl></StaticResource>`)
		write("aura/myAura/myAura.cmp", "<aura:component/>")
		write("aura/myAura/myAuraController.js", "({})")
		write("lwc/myCmp/myCmp.js", "export default {}")
		write("lwc/myCmp/myCmp.js-meta.xml", "<LightningComponentBundle/>")
		write("flows/myFlow.flow-meta.xml", "<Flow/>")

		in, skipped, err := resolveToolingComponents(deploySelection{sourceDir: dir}, "60.0")
		if err != nil {
			t.Fatalf("resolveToolingComponents: %v", err)
		}
		if len(in.Apex) != 1 || in.Apex[0].Type != "ApexClass" || in.Apex[0].Name != "Foo" {
			t.Fatalf("Apex = %+v", in.Apex)
		}
		if in.Apex[0].Body != "public class Foo {}" {
			t.Errorf("body = %q", in.Apex[0].Body)
		}
		if len(in.Static) != 1 || in.Static[0].Name != "Baz" {
			t.Fatalf("Static = %+v", in.Static)
		}
		if in.Static[0].ContentType != "text/plain" || in.Static[0].CacheControl != "Public" {
			t.Errorf("static meta = %+v", in.Static[0])
		}
		if string(in.Static[0].Body) != "hello" {
			t.Errorf("static body = %q", in.Static[0].Body)
		}
		if len(in.Aura) != 1 || in.Aura[0].Name != "myAura" || len(in.Aura[0].Files) != 2 {
			t.Fatalf("Aura = %+v", in.Aura)
		}
		// Files are sorted by DefType: COMPONENT then CONTROLLER.
		if in.Aura[0].Files[0].DefType != "COMPONENT" || in.Aura[0].Files[1].DefType != "CONTROLLER" {
			t.Errorf("aura files = %+v", in.Aura[0].Files)
		}
		if len(in.Lwc) != 1 || in.Lwc[0].Name != "myCmp" || len(in.Lwc[0].Files) != 2 {
			t.Fatalf("Lwc = %+v", in.Lwc)
		}
		// FilePath carries the full lwc/<bundle>/<file> path.
		if in.Lwc[0].Files[0].FilePath != "lwc/myCmp/myCmp.js" {
			t.Errorf("lwc file path = %q", in.Lwc[0].Files[0].FilePath)
		}
		if len(skipped) != 1 || skipped[0] != "flows/myFlow.flow" {
			t.Errorf("skipped = %v", skipped)
		}
	})

	t.Run("unsupported type errors", func(t *testing.T) {
		_, _, err := resolveToolingComponents(deploySelection{metadata: []string{"CustomObject:Account"}}, "60.0")
		if err == nil || !strings.Contains(err.Error(), "does not support CustomObject") {
			t.Fatalf("want unsupported-type error, got %v", err)
		}
	})

	t.Run("wildcard errors", func(t *testing.T) {
		_, _, err := resolveToolingComponents(deploySelection{metadata: []string{"ApexClass"}}, "60.0")
		if err == nil || !strings.Contains(err.Error(), "wildcard is not supported") {
			t.Fatalf("want wildcard error, got %v", err)
		}
	})
}

func TestAuraDef(t *testing.T) {
	cases := []struct {
		file, defType, format string
		ok                    bool
	}{
		{"myCmp.cmp", "COMPONENT", "XML", true},
		{"myApp.app", "APPLICATION", "XML", true},
		{"myEvt.evt", "EVENT", "XML", true},
		{"myIntf.intf", "INTERFACE", "XML", true},
		{"my.tokens", "TOKENS", "XML", true},
		{"myCmp.design", "DESIGN", "XML", true},
		{"myCmp.auradoc", "DOCUMENTATION", "XML", true},
		{"myCmp.svg", "SVG", "SVG", true},
		{"myCmp.css", "STYLE", "CSS", true},
		{"myCmpController.js", "CONTROLLER", "JS", true},
		{"myCmpHelper.js", "HELPER", "JS", true},
		{"myCmpRenderer.js", "RENDERER", "JS", true},
		{"myCmp.cmp-meta.xml", "", "", false},
		{"README.md", "", "", false},
	}
	for _, c := range cases {
		dt, fm, ok := auraDef(c.file)
		if ok != c.ok || dt != c.defType || fm != c.format {
			t.Errorf("auraDef(%q) = (%q,%q,%v), want (%q,%q,%v)", c.file, dt, fm, ok, c.defType, c.format, c.ok)
		}
	}
}

func TestIncompatibleWithTooling(t *testing.T) {
	if got := incompatibleWithTooling(false, false, false, "", nil); got != "" {
		t.Errorf("clean combo flagged: %q", got)
	}
	if got := incompatibleWithTooling(false, false, false, "", []string{"MyTest"}); !strings.Contains(got, "--test-level") {
		t.Errorf("tests not flagged: %q", got)
	}
	if got := incompatibleWithTooling(true, false, false, "", nil); got != "--metadata-format" {
		t.Errorf("metadata-format = %q", got)
	}
}

// execDeploy runs the deploy command with args, returning the RunE error while
// suppressing usage/error output. It only exercises validation that returns
// before any org is contacted.
func execDeploy(args ...string) error {
	cmd := newDeployCmd()
	cmd.SilenceUsage = true
	cmd.SilenceErrors = true
	cmd.SetOut(&bytes.Buffer{})
	cmd.SetErr(&bytes.Buffer{})
	cmd.SetArgs(args)
	return cmd.Execute()
}

func TestNewDeployCmd_Validation(t *testing.T) {
	tests := []struct {
		name    string
		args    []string
		wantErr string
	}{
		{name: "no selection", args: nil, wantErr: "specify what to deploy with -d, -m, or -x"},
		{name: "metadata-format without -d", args: []string{"-m", "ApexClass:Foo", "--metadata-format"}, wantErr: "--metadata-format requires -d"},
		{name: "mutually exclusive d and m", args: []string{"-d", "force-app", "-m", "ApexClass:Foo"}, wantErr: "if any flags in the group"},
		{name: "mutually exclusive m and x", args: []string{"-m", "ApexClass:Foo", "-x", "package.xml"}, wantErr: "if any flags in the group"},
		{name: "bad test level", args: []string{"-m", "ApexClass:Foo", "-l", "bogus"}, wantErr: "invalid --test-level"},
		{name: "tests need specified level", args: []string{"-m", "ApexClass:Foo", "--tests", "MyTest"}, wantErr: "--tests requires --test-level RunSpecifiedTests"},
		{name: "negative wait", args: []string{"-m", "ApexClass:Foo", "-w", "-1"}, wantErr: "--wait must be zero or positive"},
		{name: "tooling with metadata-format", args: []string{"-d", "force-app", "--tooling", "--metadata-format"}, wantErr: "--tooling cannot be combined with --metadata-format"},
		{name: "tooling with ignore-errors", args: []string{"-m", "ApexClass:Foo", "--tooling", "--ignore-errors"}, wantErr: "--tooling cannot be combined with --ignore-errors"},
		{name: "tooling with test-level", args: []string{"-m", "ApexClass:Foo", "--tooling", "-l", "RunLocalTests"}, wantErr: "--tooling cannot be combined with --test-level"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := execDeploy(tt.args...)
			if err == nil {
				t.Fatalf("want error containing %q, got nil", tt.wantErr)
			}
			if !strings.Contains(err.Error(), tt.wantErr) {
				t.Fatalf("error = %q, want containing %q", err, tt.wantErr)
			}
		})
	}
}
