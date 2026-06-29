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

// AuraFile is one definition within an Aura bundle: its DefType (COMPONENT,
// CONTROLLER, HELPER, STYLE, …), Format (XML/JS/CSS/SVG), and source text.
type AuraFile struct {
	DefType string
	Format  string
	Source  string
}

// ToolingAuraBundle is one Aura bundle to upsert (the bundle must already exist
// in the org; its definitions are created or updated per DefType).
type ToolingAuraBundle struct {
	Name  string
	Files []AuraFile
}

// LwcFile is one resource within an LWC bundle: its FilePath (e.g.
// "lwc/myCmp/myCmp.js"), Format (file extension), and source text.
type LwcFile struct {
	FilePath string
	Format   string
	Source   string
}

// ToolingLwcBundle is one Lightning web component bundle to upsert (the bundle
// must already exist in the org; its resources are created or updated by path).
type ToolingLwcBundle struct {
	Name  string
	Files []LwcFile
}

// ToolingDeployInput groups what to deploy by the mechanism each kind needs:
// Apex/Visualforce go through a MetadataContainer (compiled, check-only capable),
// while static resources, Aura, and LWC bundles are upserted as plain Tooling
// records.
type ToolingDeployInput struct {
	Apex   []ToolingComponent
	Static []ToolingStaticResource
	Aura   []ToolingAuraBundle
	Lwc    []ToolingLwcBundle
}

func (in ToolingDeployInput) isEmpty() bool {
	return len(in.Apex) == 0 && len(in.Static) == 0 && len(in.Aura) == 0 && len(in.Lwc) == 0
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
	if len(in.Aura) > 0 {
		r, err := c.deployAura(ctx, in.Aura, progress)
		if r != nil {
			res.Succeeded = append(res.Succeeded, r.Succeeded...)
			res.Errors = append(res.Errors, r.Errors...)
		}
		if err != nil {
			return res, err
		}
	}
	if len(in.Lwc) > 0 {
		r, err := c.deployLwc(ctx, in.Lwc, progress)
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

// upsertOp is one create-or-update of a Tooling sObject within a composite
// batch. label identifies it in error reporting ("StaticResource:Foo").
type upsertOp struct {
	label  string
	method string // POST (create) or PATCH (update)
	url    string // collection path for POST, record path for PATCH
	fields map[string]any
}

// runUpserts executes ops via Tooling composite with allOrNone=false (each op
// stands alone, so one bad record doesn't hide the rest), chunked to
// MaxCompositeSubrequests. It returns, per op index, nil on success or that op's
// error. A transport-level failure (not a per-op failure) returns a non-nil
// error and a nil slice.
func (c *Client) runUpserts(ctx context.Context, ops []upsertOp) ([]error, error) {
	errs := make([]error, len(ops))
	for start := 0; start < len(ops); start += MaxCompositeSubrequests {
		end := min(start+MaxCompositeSubrequests, len(ops))
		subs := make([]compositeSub, 0, end-start)
		for i := start; i < end; i++ {
			body, err := json.Marshal(ops[i].fields)
			if err != nil {
				return nil, err
			}
			subs = append(subs, compositeSub{
				Method:      ops[i].method,
				URL:         ops[i].url,
				ReferenceID: fmt.Sprintf("op%d", i-start),
				Body:        body,
			})
		}
		results, err := c.toolingComposite(ctx, false, subs)
		if err != nil {
			return nil, err
		}
		for j, r := range results {
			if r.HTTPStatusCode >= 300 {
				errs[start+j] = toolingError(r.HTTPStatusCode, r.Body)
			}
		}
	}
	return errs, nil
}

// deployLwc upserts each LWC bundle's resources directly (LWC is not
// container-deployable). The bundle must already exist; each local file becomes
// a LightningComponentResource matched to an existing row by FilePath (updated)
// or created. A bundle's files are pushed in one composite batch.
func (c *Client) deployLwc(ctx context.Context, bundles []ToolingLwcBundle, progress func(state string)) (*ToolingDeployResult, error) {
	res := &ToolingDeployResult{}
	for _, b := range bundles {
		if progress != nil {
			progress("LWC " + b.Name)
		}
		bundleID, err := c.singleID(ctx, "LightningComponentBundle", "DeveloperName", b.Name)
		if err != nil {
			return res, err
		}
		if bundleID == "" {
			res.Errors = append(res.Errors, CompileError{
				Component: "LightningComponentBundle:" + b.Name,
				Problem:   "not present in org (create it first with a Metadata API deploy, then use --tooling)",
			})
			continue
		}
		existing, err := c.lwcResourceIDs(ctx, bundleID)
		if err != nil {
			return res, err
		}

		ops := make([]upsertOp, len(b.Files))
		for i, f := range b.Files {
			if id := existing[f.FilePath]; id != "" {
				ops[i] = upsertOp{
					label:  "LightningComponentResource:" + f.FilePath,
					method: "PATCH",
					url:    c.toolingSObjectPath("LightningComponentResource", id),
					fields: map[string]any{"Source": f.Source},
				}
			} else {
				ops[i] = upsertOp{
					label:  "LightningComponentResource:" + f.FilePath,
					method: "POST",
					url:    c.toolingSObjectPath("LightningComponentResource", ""),
					fields: map[string]any{
						"LightningComponentBundleId": bundleID,
						"FilePath":                   f.FilePath,
						"Format":                     f.Format,
						"Source":                     f.Source,
					},
				}
			}
		}
		opErrs, err := c.runUpserts(ctx, ops)
		if err != nil {
			return res, err
		}
		bundleErr := false
		for i, e := range opErrs {
			if e != nil {
				res.Errors = append(res.Errors, CompileError{Component: ops[i].label, Problem: e.Error()})
				bundleErr = true
			}
		}
		if !bundleErr {
			res.Succeeded = append(res.Succeeded, "LightningComponentBundle:"+b.Name)
		}
	}
	if len(res.Errors) > 0 {
		return res, fmt.Errorf("lwc deploy failed")
	}
	return res, nil
}

// lwcResourceIDs maps each existing LightningComponentResource in a bundle to its
// id, keyed by FilePath.
func (c *Client) lwcResourceIDs(ctx context.Context, bundleID string) (map[string]string, error) {
	soql := fmt.Sprintf("SELECT Id, FilePath FROM LightningComponentResource WHERE LightningComponentBundleId = '%s'", bundleID)
	records, _, err := c.QueryTooling(ctx, soql)
	if err != nil {
		return nil, fmt.Errorf("look up lwc resources: %w", err)
	}
	out := map[string]string{}
	for _, rec := range records {
		var r struct {
			ID       string `json:"Id"`
			FilePath string `json:"FilePath"`
		}
		if err := json.Unmarshal(rec, &r); err != nil {
			return nil, err
		}
		out[r.FilePath] = r.ID
	}
	return out, nil
}

// deployAura upserts each Aura bundle's definitions directly (Aura is not
// container-deployable). The bundle must already exist in the org; each local
// file becomes an AuraDefinition matched to an existing row by DefType (updated)
// or created. Per-file/per-bundle failures are collected.
func (c *Client) deployAura(ctx context.Context, bundles []ToolingAuraBundle, progress func(state string)) (*ToolingDeployResult, error) {
	res := &ToolingDeployResult{}
	for _, b := range bundles {
		if progress != nil {
			progress("Aura " + b.Name)
		}
		bundleID, err := c.singleID(ctx, "AuraDefinitionBundle", "DeveloperName", b.Name)
		if err != nil {
			return res, err
		}
		if bundleID == "" {
			res.Errors = append(res.Errors, CompileError{
				Component: "AuraDefinitionBundle:" + b.Name,
				Problem:   "not present in org (create it first with a Metadata API deploy, then use --tooling)",
			})
			continue
		}
		existing, err := c.auraDefIDs(ctx, bundleID)
		if err != nil {
			return res, err
		}

		ops := make([]upsertOp, len(b.Files))
		for i, f := range b.Files {
			if id := existing[f.DefType]; id != "" {
				ops[i] = upsertOp{
					label:  "AuraDefinition:" + b.Name + "/" + f.DefType,
					method: "PATCH",
					url:    c.toolingSObjectPath("AuraDefinition", id),
					fields: map[string]any{"Source": f.Source},
				}
			} else {
				ops[i] = upsertOp{
					label:  "AuraDefinition:" + b.Name + "/" + f.DefType,
					method: "POST",
					url:    c.toolingSObjectPath("AuraDefinition", ""),
					fields: map[string]any{
						"AuraDefinitionBundleId": bundleID,
						"DefType":                f.DefType,
						"Format":                 f.Format,
						"Source":                 f.Source,
					},
				}
			}
		}
		opErrs, err := c.runUpserts(ctx, ops)
		if err != nil {
			return res, err
		}
		bundleErr := false
		for i, e := range opErrs {
			if e != nil {
				res.Errors = append(res.Errors, CompileError{Component: ops[i].label, Problem: e.Error()})
				bundleErr = true
			}
		}
		if !bundleErr {
			res.Succeeded = append(res.Succeeded, "AuraDefinitionBundle:"+b.Name)
		}
	}
	if len(res.Errors) > 0 {
		return res, fmt.Errorf("aura deploy failed")
	}
	return res, nil
}

// auraDefIDs maps each existing AuraDefinition in a bundle to its id, keyed by
// DefType.
func (c *Client) auraDefIDs(ctx context.Context, bundleID string) (map[string]string, error) {
	soql := fmt.Sprintf("SELECT Id, DefType FROM AuraDefinition WHERE AuraDefinitionBundleId = '%s'", bundleID)
	records, _, err := c.QueryTooling(ctx, soql)
	if err != nil {
		return nil, fmt.Errorf("look up aura definitions: %w", err)
	}
	out := map[string]string{}
	for _, rec := range records {
		var r struct {
			ID      string `json:"Id"`
			DefType string `json:"DefType"`
		}
		if err := json.Unmarshal(rec, &r); err != nil {
			return nil, err
		}
		out[r.DefType] = r.ID
	}
	return out, nil
}

// singleID returns the id of the one record of typ whose field equals value, or
// "" when none match.
func (c *Client) singleID(ctx context.Context, typ, field, value string) (string, error) {
	soql := fmt.Sprintf("SELECT Id FROM %s WHERE %s = '%s'", typ, field, strings.ReplaceAll(value, "'", "\\'"))
	records, _, err := c.QueryTooling(ctx, soql)
	if err != nil {
		return "", fmt.Errorf("look up %s: %w", typ, err)
	}
	if len(records) == 0 {
		return "", nil
	}
	var r struct {
		ID string `json:"Id"`
	}
	if err := json.Unmarshal(records[0], &r); err != nil {
		return "", err
	}
	return r.ID, nil
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

	containerID, reqID, err := c.stageApexContainer(ctx, comps, ids, checkOnly)
	if containerID != "" {
		// Containers linger on the org otherwise; clean up regardless of outcome.
		defer func() {
			_ = c.deleteSObject(context.WithoutCancel(ctx), "MetadataContainer", containerID)
		}()
	}
	if err != nil {
		return nil, err
	}

	return c.pollContainerRequest(ctx, reqID, comps, checkOnly, progress)
}

// stageApexContainer creates a MetadataContainer, stages every component as a
// <Type>Member, and submits a ContainerAsyncRequest, returning the container id
// (for cleanup) and the async request id (to poll). It collapses what used to be
// N+2 sequential round-trips into Tooling composite calls: when container(1) +
// members(N) + request(1) fit one call (the typical 1–3 class edit loop) it is a
// single all-or-none round-trip, with members chained off @{container.id}.
// Otherwise the container is created on its own, its members are chunked with the
// literal container id, and the request is submitted last.
func (c *Client) stageApexContainer(ctx context.Context, comps []ToolingComponent, ids map[string]string, checkOnly bool) (containerID, reqID string, err error) {
	containerName := fmt.Sprintf("sff_%d", time.Now().UnixNano())
	containerURL := c.toolingSObjectPath("MetadataContainer", "")
	requestURL := c.toolingSObjectPath("ContainerAsyncRequest", "")
	memberFields := func(comp ToolingComponent, parent string) map[string]any {
		return map[string]any{
			"MetadataContainerId": parent,
			"ContentEntityId":     ids[compKey(comp.Type, comp.Name)],
			"Body":                comp.Body,
		}
	}

	// container(1) + request(1) leaves room for the members in a single call.
	if len(comps) <= MaxCompositeSubrequests-2 {
		subs := []compositeSub{compositeCreate("container", containerURL, map[string]any{"Name": containerName})}
		for i, comp := range comps {
			subs = append(subs, compositeCreate(fmt.Sprintf("member%d", i),
				c.toolingSObjectPath(comp.Type+"Member", ""), memberFields(comp, "@{container.id}")))
		}
		subs = append(subs, compositeCreate("request", requestURL, map[string]any{
			"MetadataContainerId": "@{container.id}",
			"IsCheckOnly":         checkOnly,
		}))

		results, err := c.toolingComposite(ctx, true, subs)
		if err != nil {
			return "", "", err
		}
		byRef := compositeIDs(results)
		// With allOrNone a failure rolls the whole set back, but the container
		// record may still surface its id; return it so cleanup can run.
		if cerr := compositeFirstError(results); cerr != nil {
			return byRef["container"], "", fmt.Errorf("stage container: %w", cerr)
		}
		return byRef["container"], byRef["request"], nil
	}

	// Too many members for one call: create the container, then chunk the members
	// (literal id, no chaining needed), then submit the request.
	containerID, err = c.createSObject(ctx, "MetadataContainer", map[string]any{"Name": containerName})
	if err != nil {
		return "", "", fmt.Errorf("create MetadataContainer: %w", err)
	}
	for start := 0; start < len(comps); start += MaxCompositeSubrequests {
		end := min(start+MaxCompositeSubrequests, len(comps))
		subs := make([]compositeSub, 0, end-start)
		for i := start; i < end; i++ {
			comp := comps[i]
			subs = append(subs, compositeCreate(fmt.Sprintf("member%d", i),
				c.toolingSObjectPath(comp.Type+"Member", ""), memberFields(comp, containerID)))
		}
		results, err := c.toolingComposite(ctx, true, subs)
		if err != nil {
			return containerID, "", err
		}
		if cerr := compositeFirstError(results); cerr != nil {
			return containerID, "", fmt.Errorf("stage members: %w", cerr)
		}
	}
	reqID, err = c.createSObject(ctx, "ContainerAsyncRequest", map[string]any{
		"MetadataContainerId": containerID,
		"IsCheckOnly":         checkOnly,
	})
	if err != nil {
		return containerID, "", fmt.Errorf("submit ContainerAsyncRequest: %w", err)
	}
	return containerID, reqID, nil
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

	if progress != nil {
		progress(fmt.Sprintf("StaticResource (%d)", len(rs)))
	}
	ops := make([]upsertOp, len(rs))
	for i, r := range rs {
		cache := r.CacheControl
		if cache == "" {
			cache = "Private"
		}
		fields := map[string]any{
			"ContentType":  r.ContentType,
			"CacheControl": cache,
			"Body":         base64.StdEncoding.EncodeToString(r.Body),
		}
		if id := ids[r.Name]; id != "" {
			ops[i] = upsertOp{label: "StaticResource:" + r.Name, method: "PATCH", url: c.toolingSObjectPath("StaticResource", id), fields: fields}
		} else {
			fields["Name"] = r.Name
			ops[i] = upsertOp{label: "StaticResource:" + r.Name, method: "POST", url: c.toolingSObjectPath("StaticResource", ""), fields: fields}
		}
	}

	opErrs, err := c.runUpserts(ctx, ops)
	if err != nil {
		return nil, err
	}
	res := &ToolingDeployResult{}
	for i, e := range opErrs {
		if e != nil {
			res.Errors = append(res.Errors, CompileError{Component: ops[i].label, Problem: e.Error()})
			continue
		}
		res.Succeeded = append(res.Succeeded, ops[i].label)
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
