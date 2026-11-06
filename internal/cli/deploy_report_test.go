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
