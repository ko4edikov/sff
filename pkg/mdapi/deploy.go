package mdapi

import (
	"context"
	"encoding/base64"
	"encoding/xml"
	"fmt"
	"strings"
	"time"
)

// Test levels accepted by the Metadata API deploy() call.
const (
	TestLevelNone      = "NoTestRun"
	TestLevelSpecified = "RunSpecifiedTests"
	TestLevelLocal     = "RunLocalTests"
	TestLevelAll       = "RunAllTestsInOrg"
)

// DeployOptions controls a deploy. RollbackOnError is an explicit field rather
// than a default so a zero value can't silently turn off rollback; the deploy
// command always sets it true.
type DeployOptions struct {
	CheckOnly       bool     // validate only, don't save changes
	RollbackOnError bool     // undo the whole deploy if any component fails
	IgnoreWarnings  bool     // succeed even if the deploy reports warnings
	TestLevel       string   // one of the TestLevel* constants ("" = NoTestRun)
	RunTests        []string // class names for RunSpecifiedTests
}

// ComponentMessage is one component result line from a deploy (failure or success).
type ComponentMessage struct {
	FullName      string `xml:"fullName"`
	ComponentType string `xml:"componentType"`
	Problem       string `xml:"problem"`
	ProblemType   string `xml:"problemType"`
	FileName      string `xml:"fileName"`
	LineNumber    int    `xml:"lineNumber"`
	ColumnNumber  int    `xml:"columnNumber"`
	Success       bool   `xml:"success"`
}

// TestFailure is one failed Apex test from a deploy that ran tests.
type TestFailure struct {
	Name       string `xml:"name"`
	MethodName string `xml:"methodName"`
	Message    string `xml:"message"`
	StackTrace string `xml:"stackTrace"`
}

// DeployResult is the outcome of a completed (or in-progress) deploy.
type DeployResult struct {
	ID                       string
	Status                   string
	Success                  bool
	Done                     bool
	CheckOnly                bool
	ErrorMessage             string
	StateDetail              string
	NumberComponentsTotal    int
	NumberComponentsDeployed int
	NumberComponentErrors    int
	NumberTestsTotal         int
	NumberTestsCompleted     int
	NumberTestErrors         int
	ComponentFailures        []ComponentMessage
	TestFailures             []TestFailure
}

// deployStartEnv mirrors the <result> of the deploy() response (an AsyncResult).
type deployStartEnv struct {
	Result struct {
		ID    string `xml:"id"`
		Done  bool   `xml:"done"`
		State string `xml:"state"`
	} `xml:"Body>deployResponse>result"`
}

// deployStatusEnv mirrors the <result> of checkDeployStatus.
type deployStatusEnv struct {
	Result struct {
		ID                       string `xml:"id"`
		Done                     bool   `xml:"done"`
		Status                   string `xml:"status"`
		Success                  bool   `xml:"success"`
		CheckOnly                bool   `xml:"checkOnly"`
		ErrorMessage             string `xml:"errorMessage"`
		StateDetail              string `xml:"stateDetail"`
		NumberComponentsTotal    int    `xml:"numberComponentsTotal"`
		NumberComponentsDeployed int    `xml:"numberComponentsDeployed"`
		NumberComponentErrors    int    `xml:"numberComponentErrors"`
		NumberTestsTotal         int    `xml:"numberTestsTotal"`
		NumberTestsCompleted     int    `xml:"numberTestsCompleted"`
		NumberTestErrors         int    `xml:"numberTestErrors"`
		Details                  struct {
			ComponentFailures []ComponentMessage `xml:"componentFailures"`
			RunTestResult     struct {
				Failures []TestFailure `xml:"failures"`
			} `xml:"runTestResult"`
		} `xml:"details"`
	} `xml:"Body>checkDeployStatusResponse>result"`
}

// Deploy starts an async deploy of zipBytes (a singlePackage archive) and returns
// its job id.
func (c *Client) Deploy(ctx context.Context, zipBytes []byte, opts DeployOptions) (string, error) {
	body := `<met:deploy><met:ZipFile>` +
		base64.StdEncoding.EncodeToString(zipBytes) +
		`</met:ZipFile>` + deployOptionsXML(opts) + `</met:deploy>`

	raw, err := c.call(ctx, "deploy", body)
	if err != nil {
		return "", err
	}
	var env deployStartEnv
	if err := xml.Unmarshal(raw, &env); err != nil {
		return "", fmt.Errorf("parse deploy response: %w", err)
	}
	debugf("deploy response: id=%q done=%t state=%q", env.Result.ID, env.Result.Done, env.Result.State)
	if env.Result.ID == "" {
		return "", fmt.Errorf("deploy returned no job id: %s", snippet(raw))
	}
	return env.Result.ID, nil
}

// CheckDeployStatus polls a deploy job once, returning its current state.
func (c *Client) CheckDeployStatus(ctx context.Context, id string) (done bool, res *DeployResult, err error) {
	body := `<met:checkDeployStatus>` +
		`<met:asyncProcessId>` + xmlEscape(id) + `</met:asyncProcessId>` +
		`<met:includeDetails>true</met:includeDetails>` +
		`</met:checkDeployStatus>`

	raw, err := c.call(ctx, "checkDeployStatus", body)
	if err != nil {
		return false, nil, err
	}
	var env deployStatusEnv
	if err := xml.Unmarshal(raw, &env); err != nil {
		return false, nil, fmt.Errorf("parse checkDeployStatus response: %w", err)
	}
	r := env.Result
	debugf("checkDeployStatus: done=%t status=%q success=%t comps=%d/%d errs=%d tests=%d/%d testErrs=%d",
		r.Done, r.Status, r.Success, r.NumberComponentsDeployed, r.NumberComponentsTotal,
		r.NumberComponentErrors, r.NumberTestsCompleted, r.NumberTestsTotal, r.NumberTestErrors)

	out := &DeployResult{
		ID:                       r.ID,
		Status:                   r.Status,
		Success:                  r.Success,
		Done:                     r.Done,
		CheckOnly:                r.CheckOnly,
		ErrorMessage:             strings.TrimSpace(r.ErrorMessage),
		StateDetail:              strings.TrimSpace(r.StateDetail),
		NumberComponentsTotal:    r.NumberComponentsTotal,
		NumberComponentsDeployed: r.NumberComponentsDeployed,
		NumberComponentErrors:    r.NumberComponentErrors,
		NumberTestsTotal:         r.NumberTestsTotal,
		NumberTestsCompleted:     r.NumberTestsCompleted,
		NumberTestErrors:         r.NumberTestErrors,
		ComponentFailures:        r.Details.ComponentFailures,
		TestFailures:             r.Details.RunTestResult.Failures,
	}
	return r.Done, out, nil
}

// DeployAndWait starts a deploy and polls until completion. progress, if non-nil,
// is called with each polled state for UI feedback. A finished deploy that didn't
// succeed is returned alongside a non-nil error so the caller can print details.
func (c *Client) DeployAndWait(ctx context.Context, zipBytes []byte, opts DeployOptions, progress func(attempt int, res *DeployResult)) (*DeployResult, error) {
	id, err := c.Deploy(ctx, zipBytes, opts)
	if err != nil {
		return nil, err
	}

	const (
		first = 1 * time.Second
		cap_  = 5 * time.Second
	)
	// last carries at least the job id so a timed-out or cancelled wait can still
	// tell the caller which deploy is running server-side.
	last := &DeployResult{ID: id}
	wait := first
	for attempt := 1; ; attempt++ {
		select {
		case <-ctx.Done():
			return last, ctx.Err()
		case <-time.After(wait):
		}
		if wait < cap_ {
			wait += time.Second
		}

		done, res, err := c.CheckDeployStatus(ctx, id)
		if err != nil {
			return last, err
		}
		last = res
		if progress != nil {
			progress(attempt, res)
		}
		if done {
			if !res.Success {
				return res, fmt.Errorf("deploy %s", res.Status)
			}
			return res, nil
		}
	}
}

// deployOptionsXML renders the <met:DeployOptions> block. singlePackage is always
// true because sff builds a single-package zip (no -meta package directory).
func deployOptionsXML(opts DeployOptions) string {
	var b strings.Builder
	b.WriteString(`<met:DeployOptions>`)
	b.WriteString(`<met:checkOnly>` + boolXML(opts.CheckOnly) + `</met:checkOnly>`)
	b.WriteString(`<met:ignoreWarnings>` + boolXML(opts.IgnoreWarnings) + `</met:ignoreWarnings>`)
	b.WriteString(`<met:rollbackOnError>` + boolXML(opts.RollbackOnError) + `</met:rollbackOnError>`)
	b.WriteString(`<met:singlePackage>true</met:singlePackage>`)
	if tl := opts.TestLevel; tl != "" && tl != TestLevelNone {
		b.WriteString(`<met:testLevel>` + xmlEscape(tl) + `</met:testLevel>`)
		if tl == TestLevelSpecified {
			for _, t := range opts.RunTests {
				b.WriteString(`<met:runTests>` + xmlEscape(t) + `</met:runTests>`)
			}
		}
	}
	b.WriteString(`</met:DeployOptions>`)
	return b.String()
}

func boolXML(b bool) string {
	if b {
		return "true"
	}
	return "false"
}
