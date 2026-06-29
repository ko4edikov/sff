package sfapi

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
)

// QueryResult is one page of a SOQL query response.
type QueryResult struct {
	TotalSize      int               `json:"totalSize"`
	Done           bool              `json:"done"`
	NextRecordsURL string            `json:"nextRecordsUrl"`
	Records        []json.RawMessage `json:"records"`
}

// Query runs a SOQL statement against the standard REST API. When the result
// set spans multiple pages it accelerates pagination via the Composite Batch
// API (see fetchPagesComposite) instead of walking nextRecordsUrl serially.
func (c *Client) Query(ctx context.Context, soql string) ([]json.RawMessage, int, error) {
	return c.queryResource(ctx, "query", soql, true)
}

// QueryTooling runs a SOQL statement against the Tooling API (the equivalent of
// `sf data query -t`), used for metadata/setup objects such as ApexClass,
// Flow, or CustomField. Tooling pagination is walked serially: composite/batch
// is not a reliable accelerator for tooling/query locators.
func (c *Client) QueryTooling(ctx context.Context, soql string) ([]json.RawMessage, int, error) {
	return c.queryResource(ctx, "tooling/query", soql, false)
}

// queryResource fetches the first page, then either accelerates the remaining
// pages through composite/batch (when accelerate is set and there is more than
// one page) or walks nextRecordsUrl serially.
func (c *Client) queryResource(ctx context.Context, resource, soql string, accelerate bool) ([]json.RawMessage, int, error) {
	path := fmt.Sprintf("/services/data/%s/%s/?q=%s", c.APIVersion, resource, url.QueryEscape(soql))
	first, err := c.queryPage(ctx, path)
	if err != nil {
		return nil, 0, err
	}
	if first.Done || first.NextRecordsURL == "" {
		return first.Records, first.TotalSize, nil
	}

	if accelerate {
		records, err := c.fetchPagesComposite(ctx, first)
		if err != nil {
			return nil, 0, err
		}
		return records, first.TotalSize, nil
	}
	return c.fetchPagesSerial(ctx, first)
}

// queryPage GETs a single query page (the initial query or a nextRecordsUrl).
func (c *Client) queryPage(ctx context.Context, path string) (QueryResult, error) {
	var page QueryResult
	resp, err := c.do(ctx, http.MethodGet, path, nil)
	if err != nil {
		return page, err
	}
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return page, apiError(resp.StatusCode, body)
	}
	if err := json.Unmarshal(body, &page); err != nil {
		return page, fmt.Errorf("parse query response: %w", err)
	}
	return page, nil
}

// fetchPagesSerial walks nextRecordsUrl one page at a time, starting from the
// already-fetched first page.
func (c *Client) fetchPagesSerial(ctx context.Context, first QueryResult) ([]json.RawMessage, int, error) {
	all := append([]json.RawMessage(nil), first.Records...)
	path := first.NextRecordsURL
	for path != "" {
		page, err := c.queryPage(ctx, path)
		if err != nil {
			return nil, 0, err
		}
		all = append(all, page.Records...)
		if page.Done || page.NextRecordsURL == "" {
			break
		}
		path = page.NextRecordsURL
	}
	return all, first.TotalSize, nil
}

// fetchPagesComposite fetches every remaining page of a multi-page query through
// the Composite Batch API. It derives the remaining page locators from the first
// nextRecordsUrl rather than chaining requests: a locator looks like
// /services/data/v60.0/query/<id>-<offset>, where <offset> equals the page size
// (the record count of every page but the last). Knowing totalSize, all page
// URLs can be precomputed, grouped into batches of MaxBatchSubrequests, and
// dispatched concurrently (up to BatchConcurrency calls in flight), preserving
// record order.
func (c *Client) fetchPagesComposite(ctx context.Context, first QueryResult) ([]json.RawMessage, error) {
	next := first.NextRecordsURL
	dash := strings.LastIndex(next, "-")
	if dash < 0 {
		return nil, fmt.Errorf("unexpected nextRecordsUrl format: %q", next)
	}
	locatorBase := next[:dash]
	pageSize, err := strconv.Atoi(next[dash+1:])
	if err != nil || pageSize <= 0 {
		return nil, fmt.Errorf("unexpected nextRecordsUrl offset in %q", next)
	}

	// Page 2 is the real nextRecordsUrl (offset == pageSize); the rest are
	// derived from the same locator at offsets pageSize*2, pageSize*3, ...
	pageURLs := []string{next}
	for off := pageSize * 2; off < first.TotalSize; off += pageSize {
		pageURLs = append(pageURLs, fmt.Sprintf("%s-%d", locatorBase, off))
	}

	groups := chunkStrings(pageURLs, MaxBatchSubrequests)
	groupRecords := make([][]json.RawMessage, len(groups))

	conc := c.BatchConcurrency
	if conc <= 0 {
		conc = DefaultBatchConcurrency
	}

	// Bounded worker pool: a buffered channel acts as the semaphore, the first
	// error is captured and cancels the remaining work via ctx.
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	sem := make(chan struct{}, conc)
	var wg sync.WaitGroup
	var mu sync.Mutex
	var firstErr error

	for i, group := range groups {
		select {
		case <-ctx.Done():
		default:
		}
		wg.Add(1)
		sem <- struct{}{}
		go func(i int, group []string) {
			defer wg.Done()
			defer func() { <-sem }()

			records, err := c.fetchPageGroup(ctx, group)
			if err != nil {
				mu.Lock()
				if firstErr == nil {
					firstErr = err
					cancel()
				}
				mu.Unlock()
				return
			}
			groupRecords[i] = records
		}(i, group)
	}
	wg.Wait()
	if firstErr != nil {
		return nil, firstErr
	}

	all := make([]json.RawMessage, 0, first.TotalSize)
	all = append(all, first.Records...)
	for _, recs := range groupRecords {
		all = append(all, recs...)
	}
	return all, nil
}

// fetchPageGroup fetches one composite/batch's worth of pages (<= 25) and
// returns their records concatenated in subrequest order.
func (c *Client) fetchPageGroup(ctx context.Context, urls []string) ([]json.RawMessage, error) {
	subs := make([]BatchSubrequest, len(urls))
	for i, u := range urls {
		subs[i] = BatchSubrequest{Method: http.MethodGet, URL: u}
	}
	resp, err := c.CompositeBatch(ctx, BatchRequest{BatchRequests: subs})
	if err != nil {
		return nil, err
	}

	var records []json.RawMessage
	for _, sr := range resp.Results {
		if sr.StatusCode < 200 || sr.StatusCode >= 300 {
			return nil, fmt.Errorf("query page fetch failed (%d): %s", sr.StatusCode, sr.Result)
		}
		var page QueryResult
		if err := json.Unmarshal(sr.Result, &page); err != nil {
			return nil, fmt.Errorf("parse query page result: %w", err)
		}
		records = append(records, page.Records...)
	}
	return records, nil
}

// chunkStrings splits s into consecutive slices of at most size elements.
func chunkStrings(s []string, size int) [][]string {
	var chunks [][]string
	for i := 0; i < len(s); i += size {
		end := i + size
		if end > len(s) {
			end = len(s)
		}
		chunks = append(chunks, s[i:end])
	}
	return chunks
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
