package cli

import (
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
