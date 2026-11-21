package sfapi

import (
	"context"
	"encoding/json"
	"strconv"
	"sync/atomic"
	"testing"
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
