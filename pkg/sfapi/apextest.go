package sfapi

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"time"
)

// ApexTestRequest selects what the asynchronous Apex test runner executes.
// Set exactly one of TestLevel (RunLocalTests/RunAllTestsInOrg) or Tests.
type ApexTestRequest struct {
	TestLevel string         `json:"testLevel,omitempty"`
	Tests     []ApexTestItem `json:"tests,omitempty"`
}

// ApexTestItem is one test class, optionally narrowed to specific methods.
type ApexTestItem struct {
	ClassName   string   `json:"className"`
	TestMethods []string `json:"testMethods,omitempty"`
}

// ApexTestOutcome is one executed test method.
type ApexTestOutcome struct {
	ClassID    string `json:"classId"`
	ClassName  string `json:"className"`
	Method     string `json:"method"`
	Outcome    string `json:"outcome"` // Pass, Fail, CompileFail, Skip
	Message    string `json:"message,omitempty"`
	StackTrace string `json:"stackTrace,omitempty"`
	RunTimeMs  int    `json:"runTimeMs"`
}

// ApexClassCoverage is the aggregate line coverage of one class or trigger.
type ApexClassCoverage struct {
	Name           string  `json:"name"`
	LinesCovered   int     `json:"linesCovered"`
	LinesUncovered int     `json:"linesUncovered"`
	Percent        float64 `json:"percent"`
	// UncoveredLines are the line numbers no test executed, ascending.
	UncoveredLines []int `json:"uncoveredLines,omitempty"`
}

// ApexTestCoverage is the coverage one test method achieved on one class or
// trigger (a row of ApexCodeCoverage).
type ApexTestCoverage struct {
	TestClass      string  `json:"testClass"`
	TestMethod     string  `json:"testMethod"`
	Target         string  `json:"target"` // class or trigger exercised
	LinesCovered   int     `json:"linesCovered"`
	LinesUncovered int     `json:"linesUncovered"`
	Percent        float64 `json:"percent"`
}

// ApexCoverageReport is the coverage collected for a test run.
type ApexCoverageReport struct {
	Classes []ApexClassCoverage `json:"coverage"`
	PerTest []ApexTestCoverage  `json:"perTestCoverage,omitempty"`
	OrgWide float64             `json:"-"` // -1 when unavailable
}

// ApexTestRun is the finished result of a test run.
type ApexTestRun struct {
	JobID    string            `json:"jobId"`
	Tests    []ApexTestOutcome `json:"tests"`
	Failed   int               `json:"failed"`
	Passed   int               `json:"passed"`
	Skipped  int               `json:"skipped"`
	Errors   []string          `json:"errors,omitempty"` // queue-level failures (e.g. aborted classes)
	Duration time.Duration     `json:"-"`
	// SetupTimeMs and TestTimeMs come from ApexTestRunResult (TestTime falls
	// back to the sum of the method run times when unavailable).
	SetupTimeMs int `json:"setupTimeMs"`
	TestTimeMs  int `json:"testTimeMs"`
}

// ApexTestHooks are optional callbacks fired while a test run progresses.
type ApexTestHooks struct {
	// Progress receives short status strings ("2/6 class(es) done").
	Progress func(state string)
	// Queued is called once with the class names of the queued run, as soon as
	// the runner reports them.
	Queued func(classes []string)
	// ClassDone is called as soon as a class finishes, with its method
	// outcomes sorted by method name (empty if the class failed to run).
	ClassDone func(className string, tests []ApexTestOutcome)
}

// RunApexTests enqueues tests via the Tooling API (runTestsAsynchronous), polls
// until every queue item is finished, and returns the per-method outcomes.
// hooks may be nil; its callbacks run on the calling goroutine.
func (c *Client) RunApexTests(ctx context.Context, req ApexTestRequest, hooks *ApexTestHooks) (*ApexTestRun, error) {
	if hooks == nil {
		hooks = &ApexTestHooks{}
	}
	start := time.Now()
	body, err := json.Marshal(req)
	if err != nil {
		return nil, err
	}
	path := fmt.Sprintf("/services/data/%s/tooling/runTestsAsynchronous", c.APIVersion)
	status, resp, err := c.requestJSON(ctx, "POST", path, body)
	if err != nil {
		return nil, err
	}
	if status >= 300 {
		return nil, toolingError(status, resp)
	}
	var jobID string
	if err := json.Unmarshal(resp, &jobID); err != nil || jobID == "" {
		return nil, fmt.Errorf("unexpected runTestsAsynchronous response: %s", strings.TrimSpace(string(resp)))
	}

	run := &ApexTestRun{JobID: jobID}
	handled := map[string]bool{} // queue item ids already reported
	queued := false
	for delay := time.Second; ; {
		recs, _, err := c.QueryTooling(ctx, fmt.Sprintf(
			"SELECT Id, Status, ExtendedStatus, ApexClassId, ApexClass.Name FROM ApexTestQueueItem WHERE ParentJobId = '%s'", jobID))
		if err != nil {
			return nil, err
		}
		done, total := 0, len(recs)
		run.Errors = run.Errors[:0]
		var names []string
		var fresh []string               // class ids that just completed
		freshName := map[string]string{} // class id -> name
		for _, r := range recs {
			var q struct {
				ID             string `json:"Id"`
				Status         string `json:"Status"`
				ExtendedStatus string `json:"ExtendedStatus"`
				ApexClassID    string `json:"ApexClassId"`
				ApexClass      struct {
					Name string `json:"Name"`
				} `json:"ApexClass"`
			}
			if err := json.Unmarshal(r, &q); err != nil {
				return nil, err
			}
			names = append(names, q.ApexClass.Name)
			switch q.Status {
			case "Completed":
				done++
				if !handled[q.ID] {
					handled[q.ID] = true
					fresh = append(fresh, q.ApexClassID)
					freshName[q.ApexClassID] = q.ApexClass.Name
				}
			case "Failed", "Aborted":
				done++
				msg := q.Status
				if q.ExtendedStatus != "" {
					msg += ": " + q.ExtendedStatus
				}
				run.Errors = append(run.Errors, q.ApexClass.Name+" — "+msg)
			}
		}
		if !queued && total > 0 {
			queued = true
			sort.Strings(names)
			if hooks.Queued != nil {
				hooks.Queued(names)
			}
		}
		if len(fresh) > 0 {
			byClass := map[string][]ApexTestOutcome{}
			for _, chunk := range chunkStrings(uniqueStrings(fresh), 100) {
				outs, err := c.fetchTestResults(ctx, jobID, chunk)
				if err != nil {
					return nil, err
				}
				for _, o := range outs {
					byClass[o.ClassID] = append(byClass[o.ClassID], o)
				}
			}
			sort.Slice(fresh, func(i, j int) bool { return freshName[fresh[i]] < freshName[fresh[j]] })
			for _, id := range fresh {
				tests := byClass[id]
				sort.Slice(tests, func(i, j int) bool { return tests[i].Method < tests[j].Method })
				for _, t := range tests {
					switch t.Outcome {
					case "Pass":
						run.Passed++
					case "Skip":
						run.Skipped++
					default:
						run.Failed++
					}
				}
				run.Tests = append(run.Tests, tests...)
				if hooks.ClassDone != nil {
					hooks.ClassDone(freshName[id], tests)
				}
			}
		}
		if hooks.Progress != nil {
			hooks.Progress(fmt.Sprintf("%d/%d class(es) done", done, total))
		}
		if total > 0 && done == total {
			break
		}
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(delay):
		}
		if delay < 3*time.Second {
			delay += 500 * time.Millisecond
		}
	}

	sort.Slice(run.Tests, func(i, j int) bool {
		a, b := run.Tests[i], run.Tests[j]
		if a.ClassName != b.ClassName {
			return a.ClassName < b.ClassName
		}
		return a.Method < b.Method
	})
	run.TestTimeMs = 0
	for _, t := range run.Tests {
		run.TestTimeMs += t.RunTimeMs
	}
	// Best effort: TestSetupTime is missing on older API versions.
	for _, q := range []string{"SELECT TestTime, TestSetupTime", "SELECT TestTime"} {
		recs, _, err := c.QueryTooling(ctx, q+" FROM ApexTestRunResult WHERE AsyncApexJobId = '"+jobID+"'")
		if err != nil || len(recs) == 0 {
			continue
		}
		var r struct {
			TestTime      *float64 `json:"TestTime"`
			TestSetupTime *float64 `json:"TestSetupTime"`
		}
		if json.Unmarshal(recs[0], &r) == nil {
			if r.TestTime != nil && *r.TestTime > 0 {
				run.TestTimeMs = int(*r.TestTime)
			}
			if r.TestSetupTime != nil {
				run.SetupTimeMs = int(*r.TestSetupTime)
			}
			break
		}
	}
	run.Duration = time.Since(start)
	return run, nil
}

// fetchTestResults returns the ApexTestResult rows of the given test classes
// within one run.
func (c *Client) fetchTestResults(ctx context.Context, jobID string, classIDs []string) ([]ApexTestOutcome, error) {
	recs, _, err := c.QueryTooling(ctx, fmt.Sprintf(
		"SELECT ApexClassId, ApexClass.Name, MethodName, Outcome, Message, StackTrace, RunTime FROM ApexTestResult WHERE AsyncApexJobId = '%s' AND ApexClassId IN (%s)",
		jobID, quoteList(classIDs)))
	if err != nil {
		return nil, err
	}
	var out []ApexTestOutcome
	for _, r := range recs {
		var t struct {
			ApexClassID string `json:"ApexClassId"`
			ApexClass   struct {
				Name string `json:"Name"`
			} `json:"ApexClass"`
			MethodName string  `json:"MethodName"`
			Outcome    string  `json:"Outcome"`
			Message    string  `json:"Message"`
			StackTrace string  `json:"StackTrace"`
			RunTime    float64 `json:"RunTime"`
		}
		if err := json.Unmarshal(r, &t); err != nil {
			return nil, err
		}
		out = append(out, ApexTestOutcome{
			ClassID: t.ApexClassID, ClassName: t.ApexClass.Name, Method: t.MethodName,
			Outcome: t.Outcome, Message: t.Message, StackTrace: t.StackTrace, RunTimeMs: int(t.RunTime),
		})
	}
	return out, nil
}

// ApexCoverage returns the aggregate line coverage (with uncovered line
// numbers) of every class/trigger exercised by the given test classes, sorted
// by name, the per-test-method coverage, and the org-wide percentage (-1 when
// unavailable). testClassNames maps a test class id to its name.
func (c *Client) ApexCoverage(ctx context.Context, testClassNames map[string]string) (*ApexCoverageReport, error) {
	rep := &ApexCoverageReport{OrgWide: -1}
	if recs, _, err := c.QueryTooling(ctx, "SELECT PercentCovered FROM ApexOrgWideCoverage"); err == nil && len(recs) > 0 {
		var o struct {
			PercentCovered float64 `json:"PercentCovered"`
		}
		if json.Unmarshal(recs[0], &o) == nil {
			rep.OrgWide = o.PercentCovered
		}
	}

	// Per test method: what it touched (also yields the touched class ids).
	touched := map[string]bool{}
	testIDs := make([]string, 0, len(testClassNames))
	for id := range testClassNames {
		testIDs = append(testIDs, id)
	}
	sort.Strings(testIDs)
	for _, chunk := range chunkStrings(testIDs, 100) {
		recs, _, err := c.QueryTooling(ctx, "SELECT ApexTestClassId, TestMethodName, ApexClassOrTriggerId, ApexClassOrTrigger.Name, NumLinesCovered, NumLinesUncovered FROM ApexCodeCoverage WHERE ApexTestClassId IN ("+quoteList(chunk)+")")
		if err != nil {
			return rep, err
		}
		for _, r := range recs {
			var x struct {
				TestClassID string `json:"ApexTestClassId"`
				Method      string `json:"TestMethodName"`
				TargetID    string `json:"ApexClassOrTriggerId"`
				Target      struct {
					Name string `json:"Name"`
				} `json:"ApexClassOrTrigger"`
				Covered   int `json:"NumLinesCovered"`
				Uncovered int `json:"NumLinesUncovered"`
			}
			if err := json.Unmarshal(r, &x); err != nil {
				return rep, err
			}
			if x.TargetID == "" {
				continue
			}
			touched[x.TargetID] = true
			t := ApexTestCoverage{TestClass: testClassNames[x.TestClassID], TestMethod: x.Method, Target: x.Target.Name,
				LinesCovered: x.Covered, LinesUncovered: x.Uncovered}
			if n := x.Covered + x.Uncovered; n > 0 {
				t.Percent = float64(x.Covered) * 100 / float64(n)
			}
			rep.PerTest = append(rep.PerTest, t)
		}
	}
	sort.Slice(rep.PerTest, func(i, j int) bool {
		a, b := rep.PerTest[i], rep.PerTest[j]
		if a.TestClass != b.TestClass {
			return a.TestClass < b.TestClass
		}
		if a.TestMethod != b.TestMethod {
			return a.TestMethod < b.TestMethod
		}
		return a.Target < b.Target
	})

	ids := make([]string, 0, len(touched))
	for id := range touched {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	for _, chunk := range chunkStrings(ids, 100) {
		recs, _, err := c.QueryTooling(ctx, "SELECT ApexClassOrTrigger.Name, NumLinesCovered, NumLinesUncovered, Coverage FROM ApexCodeCoverageAggregate WHERE ApexClassOrTriggerId IN ("+quoteList(chunk)+")")
		if err != nil {
			return rep, err
		}
		for _, r := range recs {
			var a struct {
				Target struct {
					Name string `json:"Name"`
				} `json:"ApexClassOrTrigger"`
				Covered   int `json:"NumLinesCovered"`
				Uncovered int `json:"NumLinesUncovered"`
				Coverage  struct {
					Uncovered []int `json:"uncoveredLines"`
				} `json:"Coverage"`
			}
			if err := json.Unmarshal(r, &a); err != nil {
				return rep, err
			}
			cov := ApexClassCoverage{Name: a.Target.Name, LinesCovered: a.Covered, LinesUncovered: a.Uncovered, UncoveredLines: a.Coverage.Uncovered}
			sort.Ints(cov.UncoveredLines)
			if n := a.Covered + a.Uncovered; n > 0 {
				cov.Percent = float64(a.Covered) * 100 / float64(n)
			}
			rep.Classes = append(rep.Classes, cov)
		}
	}
	sort.Slice(rep.Classes, func(i, j int) bool { return rep.Classes[i].Name < rep.Classes[j].Name })
	return rep, nil
}

func uniqueStrings(in []string) []string {
	seen := map[string]bool{}
	var out []string
	for _, s := range in {
		if s != "" && !seen[s] {
			seen[s] = true
			out = append(out, s)
		}
	}
	return out
}

func quoteList(ids []string) string {
	q := make([]string, len(ids))
	for i, id := range ids {
		q[i] = "'" + strings.ReplaceAll(id, "'", "") + "'"
	}
	return strings.Join(q, ",")
}
