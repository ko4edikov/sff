package cli

import (
	"bytes"
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
