package sfapi

import (
	"bytes"
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/ko4edikov/sff/pkg/auth"
)

// bulkServer serves a completed job with `chunks` result chunks (3 rows each)
// via resultPages (2 links per page, to exercise nextRecordsUrl). Chunks are
// delayed inversely to their index so parallel completion is out of order.
func bulkServer(chunks int, pages bool) *httptest.Server {
	mux := http.NewServeMux()
	mux.HandleFunc("/services/data/v60.0/jobs/query", func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `{"id":"JOB1","state":"UploadComplete"}`)
	})
	mux.HandleFunc("/services/data/v60.0/jobs/query/JOB1", func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `{"id":"JOB1","state":"JobComplete"}`)
	})
	mux.HandleFunc("/services/data/v60.0/jobs/query/JOB1/resultPages", func(w http.ResponseWriter, r *http.Request) {
		if !pages {
			http.NotFound(w, r)
			return
		}
		start := 0
		fmt.Sscan(r.URL.Query().Get("from"), &start)
		var links []string
		for i := start; i < start+2 && i < chunks; i++ {
			links = append(links, fmt.Sprintf(`{"resultLink":"/jobs/query/JOB1/results?locator=c%d"}`, i))
		}
		next := "null"
		done := start+2 >= chunks
		if !done {
			next = fmt.Sprintf(`"/services/data/v60.0/jobs/query/JOB1/resultPages?from=%d"`, start+2)
		}
		fmt.Fprintf(w, `{"done":%t,"nextRecordsUrl":%s,"resultChunks":[%s]}`, done, next, strings.Join(links, ","))
	})
	mux.HandleFunc("/services/data/v60.0/jobs/query/JOB1/results", func(w http.ResponseWriter, r *http.Request) {
		var i int
		fmt.Sscanf(r.URL.Query().Get("locator"), "c%d", &i)
		time.Sleep(time.Duration(chunks-i) * 10 * time.Millisecond)
		w.Header().Set("Sforce-NumberOfRecords", "3")
		next := "null"
		if i+1 < chunks {
			next = fmt.Sprintf("c%d", i+1)
		}
		w.Header().Set("Sforce-Locator", next)
		fmt.Fprint(w, "\"Id\"\n")
		for k := 0; k < 3; k++ {
			fmt.Fprintf(w, "\"r%d-%d\"\n", i, k)
		}
	})
	return httptest.NewServer(mux)
}

func TestBulkQueryParallelOrdered(t *testing.T) {
	for _, tc := range []struct {
		name  string
		pages bool
		conc  int
	}{{"parallel", true, 4}, {"sequential-flag", true, 1}, {"fallback-no-resultPages", false, 4}} {
		t.Run(tc.name, func(t *testing.T) {
			srv := bulkServer(5, tc.pages)
			defer srv.Close()
			c := New(&auth.Org{InstanceURL: srv.URL, AccessToken: "t"})
			var buf bytes.Buffer
			rows, err := c.BulkQuery(context.Background(), "select id from x", &buf, BulkQueryOptions{PollInterval: time.Millisecond, Concurrency: tc.conc})
			if err != nil {
				t.Fatal(err)
			}
			want := "\"Id\"\n"
			for i := 0; i < 5; i++ {
				for k := 0; k < 3; k++ {
					want += fmt.Sprintf("\"r%d-%d\"\n", i, k)
				}
			}
			if buf.String() != want || rows != 15 {
				t.Fatalf("rows=%d, output:\n%s", rows, buf.String())
			}
		})
	}
}
