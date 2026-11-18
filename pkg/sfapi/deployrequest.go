package sfapi

import (
	"context"
	"encoding/json"
	"strconv"
	"strings"
	"time"
)

// DeployRequest is one row of the Tooling API's DeployRequest table — the data
// behind Setup → Deployment Status. Salesforce keeps this history for a limited
// time (about 180 days).
type DeployRequest struct {
	ID                 string    `json:"id"`
	Status             string    `json:"status"`
	CheckOnly          bool      `json:"checkOnly"`
	CreatedBy          string    `json:"createdBy"`
	Created            time.Time `json:"created"`
	Completed          time.Time `json:"completed"`
	ComponentsTotal    int       `json:"componentsTotal"`
	ComponentsDeployed int       `json:"componentsDeployed"`
	ComponentErrors    int       `json:"componentErrors"`
	TestsTotal         int       `json:"testsTotal"`
	TestsCompleted     int       `json:"testsCompleted"`
	TestErrors         int       `json:"testErrors"`
	ErrorMessage       string    `json:"errorMessage"`
}

// DeployListOptions filters ListDeployRequests. Zero values mean "no filter".
type DeployListOptions struct {
	Limit     int       // max rows (0 = 20)
	Since     time.Time // CreatedDate >= Since
	Until     time.Time // CreatedDate <= Until
	Status    string    // exact status, e.g. Succeeded, Failed, InProgress
	CheckOnly string    // "true" = validations only, "false" = real deploys only
	Username  string    // only deploys started by this username
}

// ListDeployRequests returns recent deploys, newest first.
func (c *Client) ListDeployRequests(ctx context.Context, o DeployListOptions) ([]DeployRequest, error) {
	var where []string
	if !o.Since.IsZero() {
		where = append(where, "CreatedDate >= "+SOQLTime(o.Since))
	}
	if !o.Until.IsZero() {
		where = append(where, "CreatedDate <= "+SOQLTime(o.Until))
	}
	if o.Status != "" {
		where = append(where, "Status = '"+soqlEscape(o.Status)+"'")
	}
	if o.CheckOnly != "" {
		where = append(where, "CheckOnly = "+o.CheckOnly)
	}
	if o.Username != "" {
		where = append(where, "CreatedBy.Username = '"+soqlEscape(o.Username)+"'")
	}
	limit := o.Limit
	if limit <= 0 {
		limit = 20
	}
	soql := "SELECT Id, Status, CheckOnly, CreatedBy.Name, CreatedDate, CompletedDate, " +
		"NumberComponentsTotal, NumberComponentsDeployed, NumberComponentErrors, " +
		"NumberTestsTotal, NumberTestsCompleted, NumberTestErrors, ErrorMessage FROM DeployRequest"
	if len(where) > 0 {
		soql += " WHERE " + strings.Join(where, " AND ")
	}
	soql += " ORDER BY CreatedDate DESC LIMIT " + strconv.Itoa(limit)

	recs, _, err := c.QueryTooling(ctx, soql)
	if err != nil {
		return nil, err
	}
	const layout = "2006-01-02T15:04:05.000-0700"
	out := make([]DeployRequest, 0, len(recs))
	for _, r := range recs {
		var row struct {
			ID                       string `json:"Id"`
			Status                   string
			CheckOnly                bool
			CreatedBy                *struct{ Name string }
			CreatedDate              string
			CompletedDate            string
			NumberComponentsTotal    int
			NumberComponentsDeployed int
			NumberComponentErrors    int
			NumberTestsTotal         int
			NumberTestsCompleted     int
			NumberTestErrors         int
			ErrorMessage             string
		}
		if err := json.Unmarshal(r, &row); err != nil {
			return nil, err
		}
		d := DeployRequest{
			ID: row.ID, Status: row.Status, CheckOnly: row.CheckOnly,
			ComponentsTotal: row.NumberComponentsTotal, ComponentsDeployed: row.NumberComponentsDeployed,
			ComponentErrors: row.NumberComponentErrors, TestsTotal: row.NumberTestsTotal,
			TestsCompleted: row.NumberTestsCompleted, TestErrors: row.NumberTestErrors,
			ErrorMessage: row.ErrorMessage,
		}
		if row.CreatedBy != nil {
			d.CreatedBy = row.CreatedBy.Name
		}
		d.Created, _ = time.Parse(layout, row.CreatedDate)
		d.Completed, _ = time.Parse(layout, row.CompletedDate)
		out = append(out, d)
	}
	return out, nil
}
