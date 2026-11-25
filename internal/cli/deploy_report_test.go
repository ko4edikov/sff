package cli

import (
	"bytes"
	"strings"
	"testing"

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
