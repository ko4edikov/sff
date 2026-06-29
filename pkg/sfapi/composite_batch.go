package sfapi

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
)

// MaxBatchSubrequests is the Salesforce cap on subrequests in a single
// composite/batch call.
const MaxBatchSubrequests = 25

// BatchSubrequest is one entry in a composite/batch request. These structs are
// deliberately independent of the Tooling composite types: although the wire
// shape is similar, the two APIs evolve separately and should not be coupled.
type BatchSubrequest struct {
	Method         string          `json:"method"`
	URL            string          `json:"url"`
	RichInput      json.RawMessage `json:"richInput,omitempty"`
	BinaryPartName string          `json:"binaryPartName,omitempty"`
}

// BatchRequest is the body POSTed to /composite/batch.
type BatchRequest struct {
	BatchRequests []BatchSubrequest `json:"batchRequests"`
	// HaltOnError stops processing remaining subrequests once one fails. When
	// false (the default) every subrequest is attempted and reported.
	HaltOnError bool `json:"haltOnError,omitempty"`
}

// BatchSubresult is one entry in the composite/batch response, in the same
// order as the corresponding subrequest.
type BatchSubresult struct {
	StatusCode int             `json:"statusCode"`
	Result     json.RawMessage `json:"result"`
}

// BatchResponse is the body returned by /composite/batch.
type BatchResponse struct {
	HasErrors bool             `json:"hasErrors"`
	Results   []BatchSubresult `json:"results"`
}

// CompositeBatch executes up to MaxBatchSubrequests independent subrequests in a
// single HTTP round-trip via the REST Composite Batch API. Subrequests run
// serially server-side and share the caller's context; results come back in
// request order. It is the caller's responsibility to inspect each
// BatchSubresult.StatusCode — a non-2xx subrequest does not fail the call.
func (c *Client) CompositeBatch(ctx context.Context, req BatchRequest) (*BatchResponse, error) {
	if len(req.BatchRequests) == 0 {
		return &BatchResponse{}, nil
	}
	if len(req.BatchRequests) > MaxBatchSubrequests {
		return nil, fmt.Errorf("composite/batch accepts at most %d subrequests, got %d",
			MaxBatchSubrequests, len(req.BatchRequests))
	}

	body, err := json.Marshal(req)
	if err != nil {
		return nil, fmt.Errorf("marshal composite/batch request: %w", err)
	}
	path := fmt.Sprintf("/services/data/%s/composite/batch", c.APIVersion)
	status, data, err := c.requestJSON(ctx, http.MethodPost, path, body)
	if err != nil {
		return nil, err
	}
	if status != http.StatusOK {
		return nil, apiError(status, data)
	}

	var resp BatchResponse
	if err := json.Unmarshal(data, &resp); err != nil {
		return nil, fmt.Errorf("parse composite/batch response: %w", err)
	}
	return &resp, nil
}
