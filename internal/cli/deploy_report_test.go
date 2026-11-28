package cli

import (
	"bytes"
	"strings"
	"testing"

	"github.com/ko4edikov/sff/pkg/auth"
	"github.com/ko4edikov/sff/pkg/mdapi"
)

func TestPrintDeploySuccesses(t *testing.T) {
	var b bytes.Buffer
	printDeploySuccesses(&b, []mdapi.ComponentMessage{
		{FullName: "package.xml"},
		{FullName: "Zed", ComponentType: "ApexClass", Created: true},
		{FullName: "Abc", ComponentType: "ApexClass", Changed: true},
		{FullName: "Old", ComponentType: "ApexTrigger", Deleted: true},
	})
	out := b.String()
	if strings.Contains(out, "package.xml") {
		t.Errorf("package.xml leaked:\n%s", out)
	}
	for _, want := range []string{"Abc", "Changed", "Zed", "Created", "Old", "Deleted"} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q in:\n%s", want, out)
		}
	}
	if strings.Index(out, "Abc") > strings.Index(out, "Zed") {
		t.Errorf("not sorted:\n%s", out)
	}
}

func TestPrintComponentList(t *testing.T) {
	var b strings.Builder
	printComponentList(&b, "deploying 2:", []string{"ApexClass:A", "ApexClass:B"}, []string{"ApexClass:Old"})
	want := "deploying 2:\n  ApexClass:A\n  ApexClass:B\ndeleting 1 component(s):\n  ApexClass:Old\n"
	if b.String() != want {
		t.Fatalf("got %q", b.String())
	}
	var big []string
	for i := 0; i < componentListMax+1; i++ {
		big = append(big, "ApexClass:X")
	}
	b.Reset()
	printComponentList(&b, "h", big, nil)
	if !strings.Contains(b.String(), "ApexClass") || strings.Contains(b.String(), "ApexClass:X") {
		t.Fatalf("expected collapsed counts, got %q", b.String())
	}
	b.Reset()
	printComponentList(&b, "h", nil, nil)
	if b.Len() != 0 {
		t.Fatal("empty list must print nothing")
	}
}

func TestPrintDeployReport(t *testing.T) {
	org := &auth.Org{Username: "me@x.com"}
	var b bytes.Buffer
	printDeployReport(&b, &mdapi.DeployResult{
		ID: "0Af1", Status: "Succeeded", Success: true, Done: true,
		ComponentSuccesses: []mdapi.ComponentMessage{
			{FullName: "package.xml"},
			{FullName: "A", ComponentType: "ApexClass", Changed: true},
			{FullName: "Old", ComponentType: "ApexClass", Deleted: true},
		},
	}, org, false)
	out := b.String()
	for _, want := range []string{"0Af1... Succeeded", "Deploy Info", "Deploy Options", "me@x.com", "Deployed Source", "Deleted Source", "Changed", "Deleted"} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q in:\n%s", want, out)
		}
	}
	if strings.Contains(out, "package.xml") {
		t.Errorf("package.xml leaked:\n%s", out)
	}

	b.Reset()
	printDeployReport(&b, &mdapi.DeployResult{
		ID: "0Af2", Status: "Failed", Done: true, NumberTestsTotal: 3, NumberTestsCompleted: 3, NumberTestErrors: 1,
		ComponentSuccesses: []mdapi.ComponentMessage{{FullName: "A", ComponentType: "ApexClass", Changed: true}},
		ComponentFailures:  []mdapi.ComponentMessage{{FullName: "B", ComponentType: "ApexClass", Problem: "boom", LineNumber: 3}},
		TestFailures:       []mdapi.TestFailure{{Name: "T", MethodName: "m", Message: "bad", StackTrace: "Class.T.m: line 1"}},
	}, org, false)
	out = b.String()
	if strings.Contains(out, "Deployed Source") {
		t.Errorf("rolled-back deploy must not list Deployed Source:\n%s", out)
	}
	for _, want := range []string{"Component Failures [1]", "boom", "Test Failures [1]", "stacktrace", "Class.T.m: line 1", "Passing: 2", "Failing: 1", "Total: 3"} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q in:\n%s", want, out)
		}
	}
}
