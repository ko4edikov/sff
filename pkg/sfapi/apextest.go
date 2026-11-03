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
}

// RunApexTests enqueues tests via the Tooling API (runTestsAsynchronous), polls
// until every queue item is finished, and returns the per-method outcomes.
// progress, when non-nil, receives short status strings.
func (c *Client) RunApexTests(ctx context.Context, req ApexTestRequest, progress func(state string)) (*ApexTestRun, error) {
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
	for delay := time.Second; ; {
		recs, _, err := c.QueryTooling(ctx, fmt.Sprintf(
			"SELECT Status, ExtendedStatus, ApexClass.Name FROM ApexTestQueueItem WHERE ParentJobId = '%s'", jobID))
		if err != nil {
			return nil, err
		}
		done, total := 0, len(recs)
		run.Errors = run.Errors[:0]
		for _, r := range recs {
			var q struct {
				Status         string `json:"Status"`
				ExtendedStatus string `json:"ExtendedStatus"`
				ApexClass      struct {
					Name string `json:"Name"`
				} `json:"ApexClass"`
			}
			if err := json.Unmarshal(r, &q); err != nil {
				return nil, err
			}
			switch q.Status {
			case "Completed":
				done++
			case "Failed", "Aborted":
				done++
				msg := q.Status
				if q.ExtendedStatus != "" {
					msg += ": " + q.ExtendedStatus
				}
				run.Errors = append(run.Errors, q.ApexClass.Name+" — "+msg)
			}
		}
		if progress != nil {
			progress(fmt.Sprintf("%d/%d class(es) done", done, total))
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

	recs, _, err := c.QueryTooling(ctx, fmt.Sprintf(
		"SELECT ApexClassId, ApexClass.Name, MethodName, Outcome, Message, StackTrace, RunTime FROM ApexTestResult WHERE AsyncApexJobId = '%s'", jobID))
	if err != nil {
		return nil, err
	}
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
		run.Tests = append(run.Tests, ApexTestOutcome{
			ClassID: t.ApexClassID, ClassName: t.ApexClass.Name, Method: t.MethodName,
			Outcome: t.Outcome, Message: t.Message, StackTrace: t.StackTrace, RunTimeMs: int(t.RunTime),
		})
		switch t.Outcome {
		case "Pass":
			run.Passed++
		case "Skip":
			run.Skipped++
		default:
			run.Failed++
		}
	}
	sort.Slice(run.Tests, func(i, j int) bool {
		a, b := run.Tests[i], run.Tests[j]
		if a.ClassName != b.ClassName {
			return a.ClassName < b.ClassName
		}
		return a.Method < b.Method
	})
	run.Duration = time.Since(start)
	return run, nil
}

// ApexCoverage returns the aggregate line coverage of every class/trigger
// exercised by the given test classes (found via ApexCodeCoverage), sorted by
// ascending percent, plus the org-wide percentage (-1 when unavailable).
func (c *Client) ApexCoverage(ctx context.Context, testClassIDs []string) ([]ApexClassCoverage, float64, error) {
	orgWide := -1.0
	if recs, _, err := c.QueryTooling(ctx, "SELECT PercentCovered FROM ApexOrgWideCoverage"); err == nil && len(recs) > 0 {
		var o struct {
			PercentCovered float64 `json:"PercentCovered"`
		}
		if json.Unmarshal(recs[0], &o) == nil {
			orgWide = o.PercentCovered
		}
	}

	// Classes/triggers touched by the test classes.
	touched := map[string]bool{}
	for _, chunk := range chunkStrings(uniqueStrings(testClassIDs), 100) {
		recs, _, err := c.QueryTooling(ctx, "SELECT ApexClassOrTriggerId FROM ApexCodeCoverage WHERE ApexTestClassId IN ("+quoteList(chunk)+")")
		if err != nil {
			return nil, orgWide, err
		}
		for _, r := range recs {
			var x struct {
				ID string `json:"ApexClassOrTriggerId"`
			}
			if json.Unmarshal(r, &x) == nil && x.ID != "" {
				touched[x.ID] = true
			}
		}
	}
	ids := make([]string, 0, len(touched))
	for id := range touched {
		ids = append(ids, id)
	}
	sort.Strings(ids)

	var out []ApexClassCoverage
	for _, chunk := range chunkStrings(ids, 100) {
		recs, _, err := c.QueryTooling(ctx, "SELECT ApexClassOrTrigger.Name, NumLinesCovered, NumLinesUncovered FROM ApexCodeCoverageAggregate WHERE ApexClassOrTriggerId IN ("+quoteList(chunk)+")")
		if err != nil {
			return nil, orgWide, err
		}
		for _, r := range recs {
			var a struct {
				Target struct {
					Name string `json:"Name"`
				} `json:"ApexClassOrTrigger"`
				Covered   int `json:"NumLinesCovered"`
				Uncovered int `json:"NumLinesUncovered"`
			}
			if err := json.Unmarshal(r, &a); err != nil {
				return nil, orgWide, err
			}
			cov := ApexClassCoverage{Name: a.Target.Name, LinesCovered: a.Covered, LinesUncovered: a.Uncovered}
			if n := a.Covered + a.Uncovered; n > 0 {
				cov.Percent = float64(a.Covered) * 100 / float64(n)
			}
			out = append(out, cov)
		}
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Percent != out[j].Percent {
			return out[i].Percent < out[j].Percent
		}
		return out[i].Name < out[j].Name
	})
	return out, orgWide, nil
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
