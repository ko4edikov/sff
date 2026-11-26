package cli

import (
	"bytes"
	"strings"
	"testing"

	"github.com/ko4edikov/sff/pkg/auth"
	"github.com/ko4edikov/sff/pkg/sfapi"
)

func sampleRun() testOutput {
	org := 64.4
	return testOutput{
		ApexTestRun: &sfapi.ApexTestRun{
			JobID: "707X", Passed: 1, Failed: 1, TestTimeMs: 30,
			Tests: []sfapi.ApexTestOutcome{
				{ClassName: "ATest", Method: "ok", Outcome: "Pass", RunTimeMs: 10},
				{ClassName: "ATest", Method: "bad", Outcome: "Fail", Message: "boom", StackTrace: "Class.A: line 3", RunTimeMs: 20},
			},
		},
		Coverage: []sfapi.ApexClassCoverage{
			{Name: "Svc", LinesCovered: 3, LinesUncovered: 7, UncoveredLines: []int{1, 2, 3, 4, 5, 6, 7}},
			{Name: "Full", LinesCovered: 4},
		},
		PerTestCoverage: []sfapi.ApexTestCoverage{{TestClass: "ATest", TestMethod: "ok", Target: "Svc", LinesCovered: 3, LinesUncovered: 7}},
		OrgWideCoverage: &org,
	}
}

func TestPrintTestRunFull(t *testing.T) {
	var b bytes.Buffer
	printTestRun(&b, sampleRun(), testReport{Coverage: true}, &auth.Org{OrgID: "00D", Username: "u@x"}, 0)
	got := b.String()
	for _, want := range []string{"=== Test Results", "ATest.bad", "boom", "Class.A: line 3", "=== Apex Code Coverage by Class", "1,2,3,4,5,...", "30%", "100%", "=== Test Summary", "Pass Rate", "50%", "Org Wide Coverage", "64%", "u@x"} {
		if !strings.Contains(got, want) {
			t.Errorf("missing %q in:\n%s", want, got)
		}
	}
}

func TestPrintTestRunConciseAndDetailed(t *testing.T) {
	var b bytes.Buffer
	printTestRun(&b, sampleRun(), testReport{Coverage: true, Concise: true}, &auth.Org{}, 0)
	got := b.String()
	if strings.Contains(got, "ATest.ok") || strings.Contains(got, "Coverage by Class") || !strings.Contains(got, "ATest.bad") {
		t.Errorf("concise wrong:\n%s", got)
	}
	b.Reset()
	printTestRun(&b, sampleRun(), testReport{Coverage: true, Detailed: true}, &auth.Org{}, 0)
	got = b.String()
	if !strings.Contains(got, "=== Apex Code Coverage for Test Run 707X") || !strings.Contains(got, "CLASS BEING TESTED") || !strings.Contains(got, "Svc") {
		t.Errorf("detailed wrong:\n%s", got)
	}
}

func TestPrintTableWrap(t *testing.T) {
	var b bytes.Buffer
	printTable(&b, "T", []string{"A", "MSG", "N"}, [][]string{{"x", strings.Repeat("m", 60), "1"}}, 40)
	for _, ln := range strings.Split(b.String(), "\n") {
		if len([]rune(ln)) > 40 {
			t.Errorf("line too wide (%d): %q", len([]rune(ln)), ln)
		}
	}
}
