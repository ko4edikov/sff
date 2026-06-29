package sfapi

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
)

// QueryResult is one page of a SOQL query response.
type QueryResult struct {
	TotalSize      int               `json:"totalSize"`
	Done           bool              `json:"done"`
	NextRecordsURL string            `json:"nextRecordsUrl"`
	Records        []json.RawMessage `json:"records"`
}

// Query runs a SOQL statement against the standard REST API.
func (c *Client) Query(ctx context.Context, soql string) ([]json.RawMessage, int, error) {
	return c.query(ctx, "query", soql)
}

// QueryTooling runs a SOQL statement against the Tooling API (the equivalent of
// `sf data query -t`), used for metadata/setup objects such as ApexClass,
// Flow, or CustomField.
func (c *Client) QueryTooling(ctx context.Context, soql string) ([]json.RawMessage, int, error) {
	return c.query(ctx, "tooling/query", soql)
}

// query runs a SOQL statement against the given resource ("query" or
// "tooling/query") and returns every record, following nextRecordsUrl
// pagination until the result set is exhausted.
func (c *Client) query(ctx context.Context, resource, soql string) ([]json.RawMessage, int, error) {
	path := fmt.Sprintf("/services/data/%s/%s/?q=%s", c.APIVersion, resource, url.QueryEscape(soql))

	var all []json.RawMessage
	total := 0
	for path != "" {
		resp, err := c.do(ctx, http.MethodGet, path, nil)
		if err != nil {
			return nil, 0, err
		}
		body, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			return nil, 0, apiError(resp.StatusCode, body)
		}

		var page QueryResult
		if err := json.Unmarshal(body, &page); err != nil {
			return nil, 0, fmt.Errorf("parse query response: %w", err)
		}
		all = append(all, page.Records...)
		total = page.TotalSize

		if page.Done || page.NextRecordsURL == "" {
			break
		}
		// nextRecordsUrl is already instance-relative (e.g. /services/data/...).
		path = page.NextRecordsURL
	}
	return all, total, nil
}

// Columns returns the field names of a record in their original (SELECT) order,
// excluding the synthetic "attributes" object.
func Columns(record json.RawMessage) ([]string, error) {
	dec := json.NewDecoder(bytes.NewReader(record))
	if _, err := dec.Token(); err != nil { // opening '{'
		return nil, err
	}
	var cols []string
	for dec.More() {
		keyTok, err := dec.Token()
		if err != nil {
			return nil, err
		}
		key, _ := keyTok.(string)
		var value json.RawMessage
		if err := dec.Decode(&value); err != nil { // consume the value
			return nil, err
		}
		if key == "attributes" {
			continue
		}
		cols = append(cols, key)
	}
	return cols, nil
}

// Field returns a record's value for a column as a display string. Strings are
// unquoted, null becomes "", and nested objects/arrays are rendered compactly.
func Field(record json.RawMessage, column string) string {
	var m map[string]json.RawMessage
	if err := json.Unmarshal(record, &m); err != nil {
		return ""
	}
	raw, ok := m[column]
	if !ok {
		return ""
	}
	return formatValue(raw)
}

func formatValue(raw json.RawMessage) string {
	s := strings.TrimSpace(string(raw))
	if s == "" || s == "null" {
		return ""
	}
	if s[0] == '"' {
		var str string
		if err := json.Unmarshal(raw, &str); err == nil {
			return str
		}
	}
	return s
}
