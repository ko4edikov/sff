package sfapi

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"
)

// BulkQueryStatus is a snapshot of a Bulk API 2.0 query job.
type BulkQueryStatus struct {
	ID                string `json:"id"`
	State             string `json:"state"`
	NumberRecords     int64  `json:"numberRecordsProcessed"`
	ErrorMessage      string `json:"errorMessage"`
	TotalProcessingMs int64  `json:"totalProcessingTime"`
}

// BulkQueryOptions tunes BulkQuery.
type BulkQueryOptions struct {
	// PollInterval is the delay between job status checks (default 2s).
	PollInterval time.Duration
	// MaxRecords caps rows per result chunk in the sequential fallback (0 =
	// server default). Parallel mode uses the server's own chunking.
	MaxRecords int
	// Concurrency is how many result chunks are downloaded in parallel via
	// resultPages (default DefaultBatchConcurrency; 1 = sequential).
	Concurrency int
	// OnStatus is called after each status poll.
	OnStatus func(BulkQueryStatus)
	// OnChunk is called after each result chunk is written, with the running
	// number of data rows written so far.
	OnChunk func(rows int64)
}

// BulkQuery runs soql as a Bulk API 2.0 query job and streams the CSV result to
// w (one header line, then rows). The job is created, polled until JobComplete
// and its result locators are walked chunk by chunk, so memory stays flat. If
// ctx is cancelled or an error occurs mid-way the job is aborted server-side.
// It returns the number of data rows written.
func (c *Client) BulkQuery(ctx context.Context, soql string, w io.Writer, opts BulkQueryOptions) (int64, error) {
	if opts.PollInterval <= 0 {
		opts.PollInterval = 2 * time.Second
	}
	base := fmt.Sprintf("/services/data/%s/jobs/query", c.APIVersion)

	body, _ := json.Marshal(map[string]string{
		"operation":   "query",
		"query":       soql,
		"contentType": "CSV",
		"lineEnding":  "LF",
	})
	status, data, err := c.requestJSON(ctx, http.MethodPost, base, body)
	if err != nil {
		return 0, err
	}
	if status/100 != 2 {
		return 0, apiError(status, data)
	}
	var job BulkQueryStatus
	if err := json.Unmarshal(data, &job); err != nil {
		return 0, fmt.Errorf("decode bulk job: %w", err)
	}

	done := false
	defer func() {
		if !done {
			// Best effort, on a fresh context: ctx may already be cancelled.
			actx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			_, _, _ = c.requestJSON(actx, http.MethodPatch, base+"/"+job.ID, []byte(`{"state":"Aborted"}`))
		}
	}()

	// Poll until the job finishes.
	for {
		status, data, err = c.requestJSON(ctx, http.MethodGet, base+"/"+job.ID, nil)
		if err != nil {
			return 0, err
		}
		if status/100 != 2 {
			return 0, apiError(status, data)
		}
		if err := json.Unmarshal(data, &job); err != nil {
			return 0, fmt.Errorf("decode bulk job: %w", err)
		}
		if opts.OnStatus != nil {
			opts.OnStatus(job)
		}
		switch job.State {
		case "JobComplete":
		case "Failed", "Aborted":
			done = true
			return 0, fmt.Errorf("bulk query job %s %s: %s", job.ID, strings.ToLower(job.State), job.ErrorMessage)
		default:
			select {
			case <-ctx.Done():
				return 0, ctx.Err()
			case <-time.After(opts.PollInterval):
			}
			continue
		}
		break
	}
	done = true

	links, err := c.bulkResultLinks(ctx, base, job.ID)
	if err != nil {
		return 0, err
	}
	if links == nil || opts.Concurrency == 1 {
		return c.bulkWalkLocators(ctx, base, job.ID, w, opts)
	}
	return c.bulkDownloadParallel(ctx, links, w, opts)
}

// bulkResultLinks lists the result chunk links of a completed job through
// resultPages (API 58.0+), following nextRecordsUrl until done. It returns nil
// (no error) when the resource is unavailable so the caller can fall back to
// the sequential locator walk.
func (c *Client) bulkResultLinks(ctx context.Context, base, jobID string) ([]string, error) {
	var links []string
	path := base + "/" + jobID + "/resultPages"
	for path != "" {
		status, data, err := c.requestJSON(ctx, http.MethodGet, path, nil)
		if err != nil {
			return nil, err
		}
		if status == http.StatusNotFound || status == http.StatusBadRequest && links == nil {
			return nil, nil
		}
		if status/100 != 2 {
			return nil, apiError(status, data)
		}
		var page struct {
			Done           bool    `json:"done"`
			NextRecordsURL *string `json:"nextRecordsUrl"`
			ResultChunks   []struct {
				ResultLink string `json:"resultLink"`
			} `json:"resultChunks"`
		}
		if err := json.Unmarshal(data, &page); err != nil {
			return nil, fmt.Errorf("decode resultPages: %w", err)
		}
		for _, ch := range page.ResultChunks {
			links = append(links, c.bulkPath(ch.ResultLink))
		}
		path = ""
		if !page.Done && page.NextRecordsURL != nil && *page.NextRecordsURL != "" {
			path = c.bulkPath(*page.NextRecordsURL)
		}
	}
	return links, nil
}

// bulkPath turns a link returned by the Bulk API (relative to the versioned
// data root, e.g. "/jobs/query/<id>/results?locator=..") into an absolute path.
func (c *Client) bulkPath(link string) string {
	if strings.HasPrefix(link, "/services/") {
		return link
	}
	if !strings.HasPrefix(link, "/") {
		link = "/" + link
	}
	return "/services/data/" + c.APIVersion + link
}

// chunkFile is one downloaded result chunk, parked in a temp file until the
// ordered writer reaches it.
type chunkFile struct {
	path string
	rows int64
	err  error
	held bool // a concurrency slot is held for this chunk
}

// bulkDownloadParallel downloads result chunks concurrently into temp files
// and appends them to w in order (one header line total). At most Concurrency
// chunks are on disk at once.
func (c *Client) bulkDownloadParallel(ctx context.Context, links []string, w io.Writer, opts BulkQueryOptions) (int64, error) {
	n := opts.Concurrency
	if n <= 0 {
		n = DefaultBatchConcurrency
	}
	if n > len(links) {
		n = len(links)
	}
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()

	sem := make(chan struct{}, n)
	futures := make([]chan chunkFile, len(links))
	for i := range futures {
		futures[i] = make(chan chunkFile, 1)
	}
	go func() {
		for i, link := range links {
			select {
			case sem <- struct{}{}:
			case <-ctx.Done():
				for j := i; j < len(links); j++ {
					futures[j] <- chunkFile{err: ctx.Err()}
				}
				return
			}
			go func(i int, link string) {
				f, err := c.bulkChunkToFile(ctx, link)
				f.held = true
				if err != nil {
					f.err = err
				}
				futures[i] <- f
			}(i, link)
		}
	}()

	var rows int64
	var firstErr error
	for i := range links {
		f := <-futures[i]
		if firstErr == nil && f.err != nil {
			firstErr = f.err
			cancel()
		}
		if firstErr == nil {
			if err := appendChunk(w, f.path, i == 0); err != nil {
				firstErr = err
				cancel()
			} else {
				rows += f.rows
				if opts.OnChunk != nil {
					opts.OnChunk(rows)
				}
			}
		}
		if f.path != "" {
			os.Remove(f.path)
		}
		if f.held {
			<-sem
		}
	}
	return rows, firstErr
}

// bulkChunkToFile downloads one result chunk into a temp file.
func (c *Client) bulkChunkToFile(ctx context.Context, link string) (chunkFile, error) {
	resp, err := c.bulkFetch(ctx, link)
	if err != nil {
		return chunkFile{}, err
	}
	defer resp.Body.Close()
	if resp.StatusCode/100 != 2 {
		b, _ := io.ReadAll(resp.Body)
		return chunkFile{}, apiError(resp.StatusCode, b)
	}
	tmp, err := os.CreateTemp("", "sff-bulk-*.csv")
	if err != nil {
		return chunkFile{}, err
	}
	f := chunkFile{path: tmp.Name()}
	if _, err := io.Copy(tmp, resp.Body); err != nil {
		tmp.Close()
		return f, err
	}
	if err := tmp.Close(); err != nil {
		return f, err
	}
	fmt.Sscan(resp.Header.Get("Sforce-NumberOfRecords"), &f.rows)
	return f, nil
}

// appendChunk copies a downloaded chunk to w, dropping its header line unless
// keepHeader.
func appendChunk(w io.Writer, path string, keepHeader bool) error {
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()
	br := bufio.NewReaderSize(f, 256*1024)
	header, err := br.ReadBytes('\n')
	if err != nil && err != io.EOF {
		return err
	}
	if keepHeader {
		if _, err := w.Write(header); err != nil {
			return err
		}
	}
	_, err = io.Copy(w, br)
	return err
}

// bulkWalkLocators is the sequential fallback: it follows Sforce-Locator
// chunk by chunk, streaming straight to w.
func (c *Client) bulkWalkLocators(ctx context.Context, base, jobID string, w io.Writer, opts BulkQueryOptions) (int64, error) {
	var rows int64
	locator := ""
	first := true
	for {
		path := base + "/" + jobID + "/results"
		q := url.Values{}
		if locator != "" {
			q.Set("locator", locator)
		}
		if opts.MaxRecords > 0 {
			q.Set("maxRecords", fmt.Sprint(opts.MaxRecords))
		}
		if len(q) > 0 {
			path += "?" + q.Encode()
		}
		n, next, err := c.bulkResultChunk(ctx, path, w, first)
		if err != nil {
			return rows, err
		}
		rows += n
		first = false
		if opts.OnChunk != nil {
			opts.OnChunk(rows)
		}
		if next == "" || next == "null" {
			return rows, nil
		}
		locator = next
	}
}

// bulkFetch issues an authenticated GET for a CSV result, refreshing the token
// once on a 401. The caller closes the body.
func (c *Client) bulkFetch(ctx context.Context, path string) (*http.Response, error) {
	get := func() (*http.Response, error) {
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, strings.TrimRight(c.Org.InstanceURL, "/")+path, nil)
		if err != nil {
			return nil, err
		}
		req.Header.Set("Authorization", "Bearer "+c.Org.Token())
		req.Header.Set("Accept", "text/csv")
		return c.HTTP.Do(req)
	}
	used := c.Org.Token()
	resp, err := get()
	if err != nil {
		return nil, err
	}
	if resp.StatusCode == http.StatusUnauthorized {
		resp.Body.Close()
		if rerr := c.Org.RefreshIfStale(ctx, used); rerr != nil {
			return nil, fmt.Errorf("token refresh after 401: %w", rerr)
		}
		return get()
	}
	return resp, nil
}

// bulkResultChunk streams one CSV result chunk to w, dropping its header line
// unless keepHeader. It returns the data rows written and the next locator.
func (c *Client) bulkResultChunk(ctx context.Context, path string, w io.Writer, keepHeader bool) (int64, string, error) {
	resp, err := c.bulkFetch(ctx, path)
	if err != nil {
		return 0, "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode/100 != 2 {
		b, _ := io.ReadAll(resp.Body)
		return 0, "", apiError(resp.StatusCode, b)
	}
	br := bufio.NewReaderSize(resp.Body, 256*1024)
	header, err := br.ReadBytes('\n')
	if err != nil && err != io.EOF {
		return 0, "", err
	}
	if keepHeader {
		if _, werr := w.Write(header); werr != nil {
			return 0, "", werr
		}
	}
	if _, err := io.Copy(w, br); err != nil {
		return 0, "", err
	}
	var n int64
	fmt.Sscan(resp.Header.Get("Sforce-NumberOfRecords"), &n)
	return n, resp.Header.Get("Sforce-Locator"), nil
}
