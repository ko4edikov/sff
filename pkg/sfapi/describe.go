package sfapi

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
)

// SObjectSummary is one entry of the org-wide object list (describeGlobal).
type SObjectSummary struct {
	Name          string `json:"name"`
	Label         string `json:"label"`
	KeyPrefix     string `json:"keyPrefix"`
	Custom        bool   `json:"custom"`
	Queryable     bool   `json:"queryable"`
	Updateable    bool   `json:"updateable"`
	Createable    bool   `json:"createable"`
	Deletable     bool   `json:"deletable"`
	Searchable    bool   `json:"searchable"`
	CustomSetting bool   `json:"customSetting"`
}

// PicklistValue is one option of a picklist field.
type PicklistValue struct {
	Value  string `json:"value"`
	Label  string `json:"label"`
	Active bool   `json:"active"`
}

// FieldDescribe is the subset of a field's describe metadata a query builder needs.
type FieldDescribe struct {
	Name             string          `json:"name"`
	Label            string          `json:"label"`
	Type             string          `json:"type"`
	Length           int             `json:"length"`
	Custom           bool            `json:"custom"`
	Updateable       bool            `json:"updateable"`
	Createable       bool            `json:"createable"`
	Filterable       bool            `json:"filterable"`
	Sortable         bool            `json:"sortable"`
	Calculated       bool            `json:"calculated"`
	Nillable         bool            `json:"nillable"`
	RelationshipName string          `json:"relationshipName"`
	ReferenceTo      []string        `json:"referenceTo"`
	PicklistValues   []PicklistValue `json:"picklistValues"`
}

// ChildRelationship describes a child relationship usable in a subquery.
type ChildRelationship struct {
	ChildSObject     string `json:"childSObject"`
	Field            string `json:"field"`
	RelationshipName string `json:"relationshipName"`
}

// SObjectDescribe is the describe result for one object.
type SObjectDescribe struct {
	Name               string              `json:"name"`
	Label              string              `json:"label"`
	KeyPrefix          string              `json:"keyPrefix"`
	Custom             bool                `json:"custom"`
	Queryable          bool                `json:"queryable"`
	Updateable         bool                `json:"updateable"`
	Fields             []FieldDescribe     `json:"fields"`
	ChildRelationships []ChildRelationship `json:"childRelationships"`
}

// DescribeGlobal lists every object in the org. When tooling is true it lists
// Tooling API objects instead.
func (c *Client) DescribeGlobal(ctx context.Context, tooling bool) ([]SObjectSummary, error) {
	var out struct {
		SObjects []SObjectSummary `json:"sobjects"`
	}
	path := fmt.Sprintf("%s/sobjects/", c.dataPath(tooling))
	if err := c.getJSON(ctx, path, &out); err != nil {
		return nil, err
	}
	return out.SObjects, nil
}

// Describe returns the full describe of one object (fields, relationships,
// picklists). When tooling is true the Tooling API object is described.
func (c *Client) Describe(ctx context.Context, sobject string, tooling bool) (*SObjectDescribe, error) {
	var out SObjectDescribe
	path := fmt.Sprintf("%s/sobjects/%s/describe", c.dataPath(tooling), url.PathEscape(sobject))
	if err := c.getJSON(ctx, path, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

func (c *Client) dataPath(tooling bool) string {
	if tooling {
		return fmt.Sprintf("/services/data/%s/tooling", c.APIVersion)
	}
	return fmt.Sprintf("/services/data/%s", c.APIVersion)
}

func (c *Client) getJSON(ctx context.Context, path string, v any) error {
	status, body, err := c.requestJSON(ctx, http.MethodGet, path, nil)
	if err != nil {
		return err
	}
	if status != http.StatusOK {
		return apiError(status, body)
	}
	if err := json.Unmarshal(body, v); err != nil {
		return fmt.Errorf("parse %s: %w", path, err)
	}
	return nil
}
