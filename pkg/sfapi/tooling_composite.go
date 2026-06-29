package sfapi

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
)

// MaxCompositeSubrequests is the Salesforce cap on subrequests in a single
// Tooling composite call.
const MaxCompositeSubrequests = 25

// compositeSub is one subrequest in a Tooling composite call. URL is
// instance-relative and may chain off an earlier subrequest's output with the
// @{referenceId.field} syntax; Body is the already-marshalled JSON payload.
//
// These types are deliberately independent of the REST composite/batch types in
// composite_batch.go: the Tooling composite API and the standard REST
// composite/batch API have similar wire shapes but evolve separately and must
// not be coupled.
type compositeSub struct {
	Method      string          `json:"method"`
	URL         string          `json:"url"`
	ReferenceID string          `json:"referenceId"`
	Body        json.RawMessage `json:"body,omitempty"`
}

// compositeResult is one entry in a Tooling composite response, matched to its
// subrequest by ReferenceID and returned in request order.
type compositeResult struct {
	Body           json.RawMessage `json:"body"`
	HTTPStatusCode int             `json:"httpStatusCode"`
	ReferenceID    string          `json:"referenceId"`
}

// toolingComposite executes up to MaxCompositeSubrequests subrequests in a
// single HTTP round-trip via the Tooling composite API
// (/services/data/vXX/tooling/composite). With allOrNone the whole set rolls
// back if any subrequest fails. Results come back in request order; unless
// allOrNone tripped, a failed subrequest does not fail the HTTP call, so callers
// must inspect each result's HTTPStatusCode. Subrequests may reference an earlier
// one's created id via @{referenceId.id} in a later URL or body.
func (c *Client) toolingComposite(ctx context.Context, allOrNone bool, subs []compositeSub) ([]compositeResult, error) {
	if len(subs) == 0 {
		return nil, nil
	}
	if len(subs) > MaxCompositeSubrequests {
		return nil, fmt.Errorf("tooling composite accepts at most %d subrequests, got %d",
			MaxCompositeSubrequests, len(subs))
	}

	reqBody := struct {
		AllOrNone        bool           `json:"allOrNone"`
		CompositeRequest []compositeSub `json:"compositeRequest"`
	}{AllOrNone: allOrNone, CompositeRequest: subs}

	body, err := json.Marshal(reqBody)
	if err != nil {
		return nil, fmt.Errorf("marshal tooling composite request: %w", err)
	}
	path := fmt.Sprintf("/services/data/%s/tooling/composite", c.APIVersion)
	status, data, err := c.requestJSON(ctx, http.MethodPost, path, body)
	if err != nil {
		return nil, err
	}
	if status != http.StatusOK {
		return nil, toolingError(status, data)
	}

	var resp struct {
		CompositeResponse []compositeResult `json:"compositeResponse"`
	}
	if err := json.Unmarshal(data, &resp); err != nil {
		return nil, fmt.Errorf("parse tooling composite response: %w", err)
	}
	if len(resp.CompositeResponse) != len(subs) {
		return nil, fmt.Errorf("tooling composite returned %d results for %d subrequests",
			len(resp.CompositeResponse), len(subs))
	}
	return resp.CompositeResponse, nil
}

// compositeCreate builds a POST subrequest that inserts a Tooling sObject with
// the given fields. fields is built from plain strings/bools/ids, so marshalling
// cannot fail in practice.
func compositeCreate(refID, url string, fields map[string]any) compositeSub {
	body, _ := json.Marshal(fields)
	return compositeSub{Method: http.MethodPost, URL: url, ReferenceID: refID, Body: body}
}

// compositeIDs maps each subrequest's referenceId to the id of the record it
// created (empty when the subrequest reported no id).
func compositeIDs(results []compositeResult) map[string]string {
	out := make(map[string]string, len(results))
	for _, r := range results {
		var b struct {
			ID string `json:"id"`
		}
		_ = json.Unmarshal(r.Body, &b)
		out[r.ReferenceID] = b.ID
	}
	return out
}

// compositeFirstError returns the first failed subrequest as an error, or nil
// when every subrequest succeeded. Subrequests run in order, so under allOrNone
// the first non-2xx result is the genuine failure (later ones report
// processing-halted) — exactly the one worth surfacing.
func compositeFirstError(results []compositeResult) error {
	for _, r := range results {
		if r.HTTPStatusCode >= 300 {
			return toolingError(r.HTTPStatusCode, r.Body)
		}
	}
	return nil
}
