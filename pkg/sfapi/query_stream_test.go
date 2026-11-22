package sfapi

import (
	"context"
	"encoding/json"
	"strconv"
	"sync/atomic"
	"testing"
	"time"
)

func TestQueryStream_OrderedPages(t *testing.T) {
	var calls int32
	c := compositeQueryServer(t, 10, 1000, -1, &calls)

	var got []string
	pages := 0
	err := c.QueryStream(context.Background(), "SELECT Id FROM Account", false, func(recs []json.RawMessage, total int) error {
		pages++
		if total != 1000 {
			t.Errorf("total = %d, want 1000", total)
		}
		for _, r := range recs {
			got = append(got, Field(r, "Id"))
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1000 {
		t.Fatalf("got %d records, want 1000", len(got))
	}
	for i, id := range got {
		if want := "r" + strconv.Itoa(i); id != want {
			t.Fatalf("record %d = %s, want %s (out of order)", i, id, want)
		}
	}
	if pages < 2 {
		t.Errorf("expected multiple onPage calls, got %d", pages)
	}
	if atomic.LoadInt32(&calls) == 0 {
		t.Error("expected composite batch calls")
	}
}

func TestQueryStream_AbortFromCallback(t *testing.T) {
	var calls int32
	c := compositeQueryServer(t, 10, 1000, -1, &calls)
	stop := context.Canceled
	n := 0
	err := c.QueryStream(context.Background(), "SELECT Id FROM Account", false, func([]json.RawMessage, int) error {
		n++
		if n == 2 {
			return stop
		}
		return nil
	})
	if err != stop {
		t.Fatalf("err = %v, want %v", err, stop)
	}
}

func TestQueryStream_SubrequestErrorSurfaced(t *testing.T) {
	var calls int32
	c := compositeQueryServer(t, 10, 1000, 100, &calls)
	err := c.QueryStream(context.Background(), "SELECT Id FROM Account", false, func([]json.RawMessage, int) error { return nil })
	if err == nil || err == context.Canceled {
		t.Fatalf("expected real subrequest error, got %v", err)
	}
}

// A slow consumer must back-pressure fetching: fetched-but-unemitted groups
// count against BatchConcurrency, so memory stays bounded.
func TestQueryStream_SlowConsumerBackpressure(t *testing.T) {
	var calls int32
	// pageSize 1 => one page per record, 25 pages per group; 500 records => ~20 groups.
	c := compositeQueryServer(t, 1, 500, -1, &calls)
	const conc = 2
	c.BatchConcurrency = conc

	gate := make(chan struct{})
	started := make(chan struct{})
	done := make(chan error, 1)
	n := 0
	go func() {
		done <- c.QueryStream(context.Background(), "SELECT Id FROM Account", false, func([]json.RawMessage, int) error {
			n++
			if n == 2 { // first composite group: block the consumer
				close(started)
				<-gate
			}
			return nil
		})
	}()

	<-started
	time.Sleep(300 * time.Millisecond) // let the dispatcher run as far as it is allowed to
	if got := atomic.LoadInt32(&calls); got > conc {
		t.Fatalf("with a blocked consumer %d batch calls were made, want <= %d", got, conc)
	}

	close(gate)
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("stream did not finish after consumer was released")
	}
}
