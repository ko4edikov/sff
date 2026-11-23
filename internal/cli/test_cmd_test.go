package cli

import (
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/ko4edikov/sff/pkg/sfapi"
)

func TestBuildTestRequest(t *testing.T) {
	req, err := buildTestRequest([]string{"A.m1,B", "A.m2", "C.x", "C"}, "")
	if err != nil {
		t.Fatal(err)
	}
	want := []sfapi.ApexTestItem{
		{ClassName: "A", TestMethods: []string{"m1", "m2"}},
		{ClassName: "B"},
		{ClassName: "C"},
	}
	if !reflect.DeepEqual(req.Tests, want) {
		t.Fatalf("got %+v", req.Tests)
	}
	if r, err := buildTestRequest(nil, "runlocaltests"); err != nil || r.TestLevel != "RunLocalTests" {
		t.Fatalf("level: %+v %v", r, err)
	}
	if _, err := buildTestRequest(nil, ""); err == nil {
		t.Fatal("expected error with no selection")
	}
	if _, err := buildTestRequest(nil, "bogus"); err == nil {
		t.Fatal("expected error for bad level")
	}
}

func TestLikePattern(t *testing.T) {
	if got := likePattern("My*_Te'st*"); got != `My%\_Te\'st%` {
		t.Fatalf("got %q", got)
	}
}

func TestChangedTestRequest(t *testing.T) {
	dir := t.TempDir()
	run := func(args ...string) {
		c := exec.Command("git", args...)
		c.Dir = dir
		if out, err := c.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v %s", args, err, out)
		}
	}
	write := func(rel, body string) {
		p := filepath.Join(dir, rel)
		os.MkdirAll(filepath.Dir(p), 0o755)
		if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write("sfdx-project.json", `{"packageDirectories":[{"path":"force-app","default":true}],"sourceApiVersion":"62.0"}`)
	write("force-app/main/default/classes/Old.cls", "public class Old {}")
	run("init", "-q")
	run("add", ".")
	run("-c", "user.name=t", "-c", "user.email=t@t", "commit", "-qm", "init")
	cls := "force-app/main/default/classes/"
	meta := `<?xml version="1.0"?><ApexClass xmlns="http://soap.sforce.com/2006/04/metadata"><apiVersion>62.0</apiVersion><status>Active</status></ApexClass>`
	write(cls+"FooTest.cls", "@isTest\nprivate class FooTest {}")
	write(cls+"FooTest.cls-meta.xml", meta)
	write(cls+"Foo.cls", "public class Foo {}")
	write(cls+"Foo.cls-meta.xml", meta)
	req, err := changedTestRequest(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(req.Tests) != 1 || req.Tests[0].ClassName != "FooTest" {
		t.Fatalf("got %+v", req.Tests)
	}
}
