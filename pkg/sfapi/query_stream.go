package sfapi

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"strconv"
	"strings"
	"sync"
)

// QueryStream runs a SOQL statement like Query, but hands records to onPage in
// record order as pages arrive instead of buffering the whole result. onPage is
// called first with the first page (so a UI can render immediately), then once
// per composite group (up to 25 pages) or per page when serial. totalSize is the
// server-reported total for the query. Returning an error from onPage aborts
// the remaining fetches and is returned. When tooling is true the Tooling API
// is used and pages are walked serially.
func (c *Client) QueryStream(ctx context.Context, soql string, tooling bool, onPage func(records []json.RawMessage, totalSize int) error) error {
	resource := "query"
	if tooling {
		resource = "tooling/query"
	}
	path := fmt.Sprintf("/services/data/%s/%s/?q=%s", c.APIVersion, resource, url.QueryEscape(soql))
	first, err := c.queryPage(ctx, path)
	if err != nil {
		return err
	}
	if err := onPage(first.Records, first.TotalSize); err != nil {
		return err
	}
	// Salesforce sometimes reports done=false with a nextRecordsUrl although
	// every row (totalSize) is already in hand; following it would just wait
	// for a page that never carries data.
	if first.Done || first.NextRecordsURL == "" || len(first.Records) >= first.TotalSize {
		return nil
	}

	if !tooling {
		return c.streamPagesComposite(ctx, first, func(recs []json.RawMessage) error {
			return onPage(recs, first.TotalSize)
		})
	}

	next := first.NextRecordsURL
	got := len(first.Records)
	for next != "" {
		page, err := c.queryPage(ctx, next)
		if err != nil {
			return err
		}
		if err := onPage(page.Records, first.TotalSize); err != nil {
			return err
		}
		got += len(page.Records)
		if page.Done || got >= first.TotalSize {
			break
		}
		next = page.NextRecordsURL
	}
	return nil
}

// streamPagesComposite is the streaming twin of fetchPagesComposite: same page
// URL derivation, same composite/batch groups and BatchConcurrency bound, but
// each group's records are handed to emit strictly in record order as soon as
// that group and all earlier ones have arrived, instead of being collected.
// A fetch error or an error returned by emit cancels the remaining work.
//
// A concurrency slot is held from the start of a group's fetch until its emit
// returns, so fetched-but-not-yet-emitted groups count against the limit too:
// at most BatchConcurrency groups are in memory at once, and a slow consumer
// back-pressures the fetching. This cannot deadlock: slots are taken in group
// order, so the group emit is waiting for always already owns one.
func (c *Client) streamPagesComposite(ctx context.Context, first QueryResult, emit func([]json.RawMessage) error) error {
	next := first.NextRecordsURL
	dash := strings.LastIndex(next, "-")
	if dash < 0 {
		return fmt.Errorf("unexpected nextRecordsUrl format: %q", next)
	}
	locatorBase := next[:dash]
	pageSize, err := strconv.Atoi(next[dash+1:])
	if err != nil || pageSize <= 0 {
		return fmt.Errorf("unexpected nextRecordsUrl offset in %q", next)
	}

	pageURLs := []string{next}
	for off := pageSize * 2; off < first.TotalSize; off += pageSize {
		pageURLs = append(pageURLs, fmt.Sprintf("%s-%d", locatorBase, off))
	}
	groups := chunkRamp(pageURLs, MaxBatchSubrequests)

	type groupResult struct {
		records []json.RawMessage
		err     error
	}
	results := make([]chan groupResult, len(groups))
	for i := range results {
		results[i] = make(chan groupResult, 1)
	}

	conc := c.BatchConcurrency
	if conc <= 0 {
		conc = DefaultBatchConcurrency
	}

	ctx, cancel := context.WithCancel(ctx)
	defer cancel()

	// The first worker failure is recorded before cancelling, so groups that
	// then report ctx.Err() don't mask the real cause.
	var mu sync.Mutex
	var workerErr error
	fail := func(err error) {
		mu.Lock()
		if workerErr == nil {
			workerErr = err
		}
		mu.Unlock()
		cancel()
	}
	cause := func(err error) error {
		mu.Lock()
		defer mu.Unlock()
		if workerErr != nil {
			return workerErr
		}
		return err
	}

	// Dispatcher: a buffered channel acts as the semaphore. Slots are released
	// by the consumer loop below, after emit.
	sem := make(chan struct{}, conc)
	go func() {
		for i, group := range groups {
			select {
			case sem <- struct{}{}:
			case <-ctx.Done():
				for j := i; j < len(groups); j++ {
					results[j] <- groupResult{err: ctx.Err()}
				}
				return
			}
			go func(i int, group []string) {
				recs, err := c.fetchPageGroup(ctx, group)
				if err != nil {
					fail(err)
				}
				results[i] <- groupResult{records: recs, err: err}
			}(i, group)
		}
	}()

	for i := range groups {
		r := <-results[i]
		if r.err != nil {
			return cause(r.err)
		}
		err := emit(r.records)
		<-sem // release only now: the group is written and can be dropped
		if err != nil {
			return err
		}
	}
	return nil
}

// chunkRamp splits s into groups that start small and double up to max (1, 2, 4, 8, 16, 25, 25, ...).
// A group is emitted only when ALL its pages have arrived, so with full-size groups a wide result
// shows nothing after the first page until a 25-page response (tens of MB, many seconds) is complete.
// Small early groups bring rows within about one page's latency; the later full-size groups keep the throughput.
func chunkRamp(s []string, max int) [][]string {
	var chunks [][]string
	size := 1
	for i := 0; i < len(s); {
		end := i + size
		if end > len(s) {
			end = len(s)
		}
		chunks = append(chunks, s[i:end])
		i = end
		if size < max {
			size *= 2
			if size > max {
				size = max
			}
		}
	}
	return chunks
}
