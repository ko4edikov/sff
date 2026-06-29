package sfapi

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/ko4edikov/sff/pkg/auth"
)

// pageJSON renders a query page covering records [start, start+pageSize) of a
// total-record result set, using the same nextRecordsUrl shape Salesforce emits.
func pageJSON(start, pageSize, total int) string {
	end := start + pageSize
	if end > total {
		end = total
	}
	recs := make([]string, 0, end-start)
	for i := start; i < end; i++ {
		recs = append(recs, fmt.Sprintf(`{"Id":"r%d"}`, i))
	}
	done := end >= total
	next := ""
	if !done {
		next = fmt.Sprintf("/services/data/v60.0/query/01gLOC-%d", end)
	}
	return fmt.Sprintf(`{"totalSize":%d,"done":%t,"nextRecordsUrl":%q,"records":[%s]}`,
		total, done, next, strings.Join(recs, ","))
}

func offsetOf(t *testing.T, url string) int {
	dash := strings.LastIndex(url, "-")
	if dash < 0 {
		t.Fatalf("no offset in url %q", url)
	}
	off, err := strconv.Atoi(url[dash+1:])
	if err != nil {
		t.Fatalf("bad offset in url %q: %v", url, err)
	}
	return off
}

// compositeQueryServer serves a paginated query: the initial GET returns the
// first page, and every composite/batch POST is answered with one page per
// subrequest, derived from the requested locator offset. failOffset, when >= 0,
// makes the matching subrequest come back as a 400.
func compositeQueryServer(t *testing.T, pageSize, total, failOffset int, batchCalls *int32) *Client {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.Method == http.MethodGet && strings.Contains(r.URL.Path, "/query") && r.URL.RawQuery != "":
			_, _ = w.Write([]byte(pageJSON(0, pageSize, total)))
		case r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/composite/batch"):
			atomic.AddInt32(batchCalls, 1)
			var req BatchRequest
			if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
				t.Errorf("decode batch body: %v", err)
			}
			if len(req.BatchRequests) > MaxBatchSubrequests {
				t.Errorf("group exceeded %d subrequests: %d", MaxBatchSubrequests, len(req.BatchRequests))
			}
			results := make([]string, 0, len(req.BatchRequests))
			for _, sub := range req.BatchRequests {
				off := offsetOf(t, sub.URL)
				if off == failOffset {
					results = append(results, `{"statusCode":400,"result":[{"message":"boom"}]}`)
					continue
				}
				results = append(results, fmt.Sprintf(`{"statusCode":200,"result":%s}`, pageJSON(off, pageSize, total)))
			}
			_, _ = fmt.Fprintf(w, `{"hasErrors":false,"results":[%s]}`, strings.Join(results, ","))
		default:
			t.Errorf("unexpected request %s %s", r.Method, r.URL.String())
			w.WriteHeader(http.StatusInternalServerError)
		}
	}))
	t.Cleanup(srv.Close)
	return &Client{
		Org:        &auth.Org{InstanceURL: srv.URL, AccessToken: "tok", Username: "me@test"},
		APIVersion: "v60.0",
		HTTP:       srv.Client(),
	}
}

func recordIDs(t *testing.T, recs []json.RawMessage) []string {
	t.Helper()
	out := make([]string, len(recs))
	for i, r := range recs {
		var m struct {
			ID string `json:"Id"`
		}
		if err := json.Unmarshal(r, &m); err != nil {
			t.Fatalf("unmarshal record: %v", err)
		}
		out[i] = m.ID
	}
	return out
}

func TestQuery_SinglePageNoBatch(t *testing.T) {
	var batchCalls int32
	c := compositeQueryServer(t, 200, 3, -1, &batchCalls)

	recs, total, err := c.Query(context.Background(), "SELECT Id FROM Account")
	if err != nil {
		t.Fatalf("Query: %v", err)
	}
	if total != 3 || len(recs) != 3 {
		t.Fatalf("got total=%d len=%d, want 3/3", total, len(recs))
	}
	if batchCalls != 0 {
		t.Errorf("composite/batch was called %d times for a single-page result", batchCalls)
	}
}

func TestQuery_CompositePreservesOrderAcrossGroups(t *testing.T) {
	// pageSize 1, total 30 => 29 remaining pages => 2 groups (25 + 4).
	for _, conc := range []int{1, 10} {
		var batchCalls int32
		c := compositeQueryServer(t, 1, 30, -1, &batchCalls)
		c.BatchConcurrency = conc

		recs, total, err := c.Query(context.Background(), "SELECT Id FROM Account")
		if err != nil {
			t.Fatalf("conc=%d Query: %v", conc, err)
		}
		if total != 30 || len(recs) != 30 {
			t.Fatalf("conc=%d got total=%d len=%d, want 30/30", conc, total, len(recs))
		}
		ids := recordIDs(t, recs)
		for i, id := range ids {
			if want := fmt.Sprintf("r%d", i); id != want {
				t.Fatalf("conc=%d record %d = %s, want %s (order not preserved)", conc, i, id, want)
			}
		}
		if batchCalls != 2 {
			t.Errorf("conc=%d composite/batch called %d times, want 2", conc, batchCalls)
		}
	}
}

func TestQuery_CompositeSubrequestError(t *testing.T) {
	var batchCalls int32
	c := compositeQueryServer(t, 2, 6, 4, &batchCalls) // page at offset 4 fails

	_, _, err := c.Query(context.Background(), "SELECT Id FROM Account")
	if err == nil {
		t.Fatal("expected error from failing subrequest, got nil")
	}
	if !strings.Contains(err.Error(), "400") {
		t.Errorf("error = %v, want it to mention status 400", err)
	}
}

func TestCompositeBatch_TooManySubrequests(t *testing.T) {
	var batchCalls int32
	c := compositeQueryServer(t, 1, 1, -1, &batchCalls)

	subs := make([]BatchSubrequest, MaxBatchSubrequests+1)
	_, err := c.CompositeBatch(context.Background(), BatchRequest{BatchRequests: subs})
	if err == nil {
		t.Fatal("expected error for >25 subrequests, got nil")
	}
}

func TestCompositeBatch_Empty(t *testing.T) {
	var batchCalls int32
	c := compositeQueryServer(t, 1, 1, -1, &batchCalls)

	resp, err := c.CompositeBatch(context.Background(), BatchRequest{})
	if err != nil {
		t.Fatalf("CompositeBatch empty: %v", err)
	}
	if len(resp.Results) != 0 {
		t.Errorf("expected no results, got %d", len(resp.Results))
	}
	if batchCalls != 0 {
		t.Errorf("empty batch should not hit the network, got %d calls", batchCalls)
	}
}
