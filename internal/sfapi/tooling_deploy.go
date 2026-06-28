package sfapi

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"time"
)

// ToolingComponent is one Apex/Visualforce source body to push via the Tooling
// API container flow. Type is the Tooling sObject name (ApexClass, ApexTrigger,
// ApexPage, or ApexComponent) and Body is the file contents.
type ToolingComponent struct {
	Type string
	Name string
	Body string
}

// ToolingStaticResource is one static resource to upsert directly (static
// resources are not container-deployable). Body is the raw .resource bytes.
type ToolingStaticResource struct {
	Name         string
	ContentType  string
	CacheControl string // "Public" or "Private"
	Body         []byte
}

// ToolingDeployInput groups what to deploy by the mechanism each kind needs:
// Apex/Visualforce go through a MetadataContainer (compiled, check-only capable),
// while static resources are upserted as plain Tooling records.
type ToolingDeployInput struct {
	Apex   []ToolingComponent
	Static []ToolingStaticResource
}

func (in ToolingDeployInput) isEmpty() bool {
	return len(in.Apex) == 0 && len(in.Static) == 0
}

// CompileError is one component failure reported by a container deploy.
type CompileError struct {
	Component string // "Type:Name", best-effort from the failure's fullName
	Problem   string
	Line      int
	Column    int
}

// ToolingDeployResult is the outcome of a container deploy.
type ToolingDeployResult struct {
	CheckOnly bool
	Succeeded []string // "Type:Name" of components that compiled
	Errors    []CompileError
}

// ToolingDeploy deploys the input through the Tooling API, routing Apex/VF
// through a MetadataContainer and static resources through direct upserts.
// checkOnly only applies to the container path (the caller rejects --check-only
// when static resources are present). progress, if non-nil, is called with a
// status string. Results from both mechanisms are merged; the first failing
// mechanism returns its partial result alongside a non-nil error.
func (c *Client) ToolingDeploy(ctx context.Context, in ToolingDeployInput, checkOnly bool, progress func(state string)) (*ToolingDeployResult, error) {
	if in.isEmpty() {
		return nil, fmt.Errorf("nothing to deploy")
	}
	res := &ToolingDeployResult{CheckOnly: checkOnly}

	if len(in.Apex) > 0 {
		r, err := c.deployApexContainer(ctx, in.Apex, checkOnly, progress)
		if r != nil {
			res.Succeeded = append(res.Succeeded, r.Succeeded...)
			res.Errors = append(res.Errors, r.Errors...)
		}
		if err != nil {
			return res, err
		}
	}
	if len(in.Static) > 0 {
		r, err := c.upsertStaticResources(ctx, in.Static, progress)
		if r != nil {
			res.Succeeded = append(res.Succeeded, r.Succeeded...)
			res.Errors = append(res.Errors, r.Errors...)
		}
		if err != nil {
			return res, err
		}
	}
	return res, nil
}

// deployApexContainer compiles and saves comps through a MetadataContainer +
// ContainerAsyncRequest (the Tooling API "fast deploy" used by the old IDEs).
// Every component must already exist in the org — the container references each
// by ContentEntityId — so a missing one is reported as an error rather than
// created. checkOnly maps to ContainerAsyncRequest.IsCheckOnly (compile only,
// don't save). A completed-but-failed deploy is returned alongside a non-nil
// error so the caller can print compiler errors.
func (c *Client) deployApexContainer(ctx context.Context, comps []ToolingComponent, checkOnly bool, progress func(state string)) (*ToolingDeployResult, error) {
	ids, err := c.toolingEntityIDs(ctx, comps)
	if err != nil {
		return nil, err
	}
	var missing []string
	for _, comp := range comps {
		if ids[compKey(comp.Type, comp.Name)] == "" {
			missing = append(missing, comp.Type+":"+comp.Name)
		}
	}
	if len(missing) > 0 {
		sort.Strings(missing)
		return nil, fmt.Errorf("not present in org (create them first with a Metadata API deploy, then use --tooling): %s",
			strings.Join(missing, ", "))
	}

	containerID, err := c.createSObject(ctx, "MetadataContainer", map[string]any{
		"Name": fmt.Sprintf("sff_%d", time.Now().UnixNano()),
	})
	if err != nil {
		return nil, fmt.Errorf("create MetadataContainer: %w", err)
	}
	// Containers linger on the org otherwise; clean up regardless of outcome.
	defer func() {
		_ = c.deleteSObject(context.WithoutCancel(ctx), "MetadataContainer", containerID)
	}()

	for _, comp := range comps {
		if _, err := c.createSObject(ctx, comp.Type+"Member", map[string]any{
			"MetadataContainerId": containerID,
			"ContentEntityId":     ids[compKey(comp.Type, comp.Name)],
			"Body":                comp.Body,
		}); err != nil {
			return nil, fmt.Errorf("stage %s:%s: %w", comp.Type, comp.Name, err)
		}
	}

	reqID, err := c.createSObject(ctx, "ContainerAsyncRequest", map[string]any{
		"MetadataContainerId": containerID,
		"IsCheckOnly":         checkOnly,
	})
	if err != nil {
		return nil, fmt.Errorf("submit ContainerAsyncRequest: %w", err)
	}

	return c.pollContainerRequest(ctx, reqID, comps, checkOnly, progress)
}

// pollContainerRequest waits for a ContainerAsyncRequest to finish, mapping its
// terminal state into a ToolingDeployResult.
func (c *Client) pollContainerRequest(ctx context.Context, reqID string, comps []ToolingComponent, checkOnly bool, progress func(state string)) (*ToolingDeployResult, error) {
	const (
		first = 500 * time.Millisecond
		cap_  = 3 * time.Second
	)
	wait := first
	for {
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(wait):
		}
		if wait < cap_ {
			wait += 500 * time.Millisecond
		}

		car, err := c.getContainerRequest(ctx, reqID)
		if err != nil {
			return nil, err
		}
		if progress != nil {
			progress(car.State)
		}

		switch car.State {
		case "Queued", "Invalidated", "":
			continue
		case "Completed":
			res := &ToolingDeployResult{CheckOnly: checkOnly}
			for _, comp := range comps {
				res.Succeeded = append(res.Succeeded, comp.Type+":"+comp.Name)
			}
			return res, nil
		default: // Failed, Error, Aborted
			res := &ToolingDeployResult{CheckOnly: checkOnly, Errors: car.compileErrors()}
			msg := car.ErrorMsg
			if msg == "" {
				msg = car.State
			}
			return res, fmt.Errorf("tooling deploy %s", strings.ToLower(msg))
		}
	}
}

// containerAsyncRequest mirrors the fields of a ContainerAsyncRequest record.
type containerAsyncRequest struct {
	State    string `json:"State"`
	ErrorMsg string `json:"ErrorMsg"`
	Details  struct {
		ComponentFailures []struct {
			FullName     string `json:"fullName"`
			Problem      string `json:"problem"`
			LineNumber   int    `json:"lineNumber"`
			ColumnNumber int    `json:"columnNumber"`
			Success      bool   `json:"success"`
		} `json:"componentFailures"`
	} `json:"DeployDetails"`
}

// compileErrors flattens the request's component failures into CompileErrors.
func (r *containerAsyncRequest) compileErrors() []CompileError {
	var out []CompileError
	for _, f := range r.Details.ComponentFailures {
		if f.Success {
			continue
		}
		out = append(out, CompileError{
			Component: f.FullName,
			Problem:   strings.TrimSpace(f.Problem),
			Line:      f.LineNumber,
			Column:    f.ColumnNumber,
		})
	}
	return out
}

func (c *Client) getContainerRequest(ctx context.Context, id string) (*containerAsyncRequest, error) {
	path := c.toolingSObjectPath("ContainerAsyncRequest", id) +
		"?fields=State,ErrorMsg,DeployDetails"
	status, body, err := c.requestJSON(ctx, "GET", path, nil)
	if err != nil {
		return nil, err
	}
	if status >= 300 {
		return nil, toolingError(status, body)
	}
	var car containerAsyncRequest
	if err := json.Unmarshal(body, &car); err != nil {
		return nil, fmt.Errorf("parse ContainerAsyncRequest: %w", err)
	}
	return &car, nil
}

// toolingEntityIDs queries the org for the record id of each component, keyed by
// "Type:Name". Components absent from the org are simply missing from the map.
func (c *Client) toolingEntityIDs(ctx context.Context, comps []ToolingComponent) (map[string]string, error) {
	byType := map[string][]string{}
	for _, comp := range comps {
		byType[comp.Type] = append(byType[comp.Type], comp.Name)
	}

	ids := map[string]string{}
	for typ, names := range byType {
		found, err := c.recordIDsByName(ctx, typ, names)
		if err != nil {
			return nil, err
		}
		for name, id := range found {
			ids[compKey(typ, name)] = id
		}
	}
	return ids, nil
}

// recordIDsByName returns the id of each named record of a Tooling type, keyed
// by Name. Names absent from the org are simply missing from the map.
func (c *Client) recordIDsByName(ctx context.Context, typ string, names []string) (map[string]string, error) {
	quoted := make([]string, len(names))
	for i, n := range names {
		quoted[i] = "'" + strings.ReplaceAll(n, "'", "\\'") + "'"
	}
	soql := fmt.Sprintf("SELECT Id, Name FROM %s WHERE Name IN (%s)", typ, strings.Join(quoted, ","))
	records, _, err := c.QueryTooling(ctx, soql)
	if err != nil {
		return nil, fmt.Errorf("look up %s ids: %w", typ, err)
	}
	ids := map[string]string{}
	for _, rec := range records {
		var r struct {
			ID   string `json:"Id"`
			Name string `json:"Name"`
		}
		if err := json.Unmarshal(rec, &r); err != nil {
			return nil, err
		}
		ids[r.Name] = r.ID
	}
	return ids, nil
}

// upsertStaticResources creates or updates each static resource directly (they
// are not container-deployable). Per-resource failures are collected so one bad
// resource doesn't hide the rest.
func (c *Client) upsertStaticResources(ctx context.Context, rs []ToolingStaticResource, progress func(state string)) (*ToolingDeployResult, error) {
	names := make([]string, len(rs))
	for i, r := range rs {
		names[i] = r.Name
	}
	ids, err := c.recordIDsByName(ctx, "StaticResource", names)
	if err != nil {
		return nil, err
	}

	res := &ToolingDeployResult{}
	for _, r := range rs {
		if progress != nil {
			progress("StaticResource " + r.Name)
		}
		cache := r.CacheControl
		if cache == "" {
			cache = "Private"
		}
		fields := map[string]any{
			"ContentType":  r.ContentType,
			"CacheControl": cache,
			"Body":         base64.StdEncoding.EncodeToString(r.Body),
		}
		var derr error
		if id := ids[r.Name]; id != "" {
			derr = c.updateSObject(ctx, "StaticResource", id, fields)
		} else {
			fields["Name"] = r.Name
			_, derr = c.createSObject(ctx, "StaticResource", fields)
		}
		if derr != nil {
			res.Errors = append(res.Errors, CompileError{Component: "StaticResource:" + r.Name, Problem: derr.Error()})
			continue
		}
		res.Succeeded = append(res.Succeeded, "StaticResource:"+r.Name)
	}
	if len(res.Errors) > 0 {
		return res, fmt.Errorf("static resource deploy failed")
	}
	return res, nil
}

// createSObject inserts a Tooling sObject and returns its new id.
func (c *Client) createSObject(ctx context.Context, typ string, fields map[string]any) (string, error) {
	body, err := json.Marshal(fields)
	if err != nil {
		return "", err
	}
	status, resp, err := c.requestJSON(ctx, "POST", c.toolingSObjectPath(typ, ""), body)
	if err != nil {
		return "", err
	}
	if status >= 300 {
		return "", toolingError(status, resp)
	}
	var out struct {
		ID      string `json:"id"`
		Success bool   `json:"success"`
	}
	if err := json.Unmarshal(resp, &out); err != nil {
		return "", fmt.Errorf("parse create response: %w", err)
	}
	if out.ID == "" {
		return "", fmt.Errorf("create %s returned no id: %s", typ, strings.TrimSpace(string(resp)))
	}
	return out.ID, nil
}

// updateSObject patches the given fields onto an existing Tooling sObject.
func (c *Client) updateSObject(ctx context.Context, typ, id string, fields map[string]any) error {
	body, err := json.Marshal(fields)
	if err != nil {
		return err
	}
	status, resp, err := c.requestJSON(ctx, "PATCH", c.toolingSObjectPath(typ, id), body)
	if err != nil {
		return err
	}
	if status >= 300 {
		return toolingError(status, resp)
	}
	return nil
}

// deleteSObject removes a Tooling sObject; used for best-effort cleanup.
func (c *Client) deleteSObject(ctx context.Context, typ, id string) error {
	status, resp, err := c.requestJSON(ctx, "DELETE", c.toolingSObjectPath(typ, id), nil)
	if err != nil {
		return err
	}
	if status >= 300 {
		return toolingError(status, resp)
	}
	return nil
}

// toolingSObjectPath builds the REST path for a Tooling sObject collection (id
// empty) or a single record.
func (c *Client) toolingSObjectPath(typ, id string) string {
	base := fmt.Sprintf("/services/data/%s/tooling/sobjects/%s", c.APIVersion, typ)
	if id == "" {
		return base
	}
	return base + "/" + id
}

func compKey(typ, name string) string { return typ + ":" + name }

// toolingError turns a Salesforce error response (a JSON array of
// {message,errorCode}) into a readable error, falling back to the raw body.
func toolingError(status int, body []byte) error {
	var errs []struct {
		Message   string `json:"message"`
		ErrorCode string `json:"errorCode"`
	}
	if json.Unmarshal(body, &errs) == nil && len(errs) > 0 {
		msgs := make([]string, len(errs))
		for i, e := range errs {
			msgs[i] = e.Message
		}
		return fmt.Errorf("salesforce API error (%d): %s", status, strings.Join(msgs, "; "))
	}
	return apiError(status, body)
}
