package cli

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"
)

func recs(ss ...string) []json.RawMessage {
	var out []json.RawMessage
	for _, s := range ss {
		out = append(out, json.RawMessage(s))
	}
	return out
}

func TestJSONSinkMatchesWriteJSON(t *testing.T) {
	r := recs(`{"attributes":{"type":"Account"},"Id":"1","Owner":{"Name":"A"}}`, `{"Id":"2","N":null}`, `{"Id":"3"}`)
	for _, tc := range [][]json.RawMessage{r, nil} {
		var want bytes.Buffer
		if err := writeJSON(&want, tc, 7); err != nil {
			t.Fatal(err)
		}
		var got bytes.Buffer
		s := &jsonSink{w: &got}
		s.begin(7)
		// split across two writes to exercise comma handling
		if len(tc) > 1 {
			s.write(tc[:1])
			s.write(tc[1:])
		} else {
			s.write(tc)
		}
		s.end(7)
		if got.String() != want.String() {
			t.Errorf("mismatch\n got: %q\nwant: %q", got.String(), want.String())
		}
	}
}

func TestCSVSinkMatchesWriteCSVAndWarns(t *testing.T) {
	r := recs(`{"Id":"1","Name":"a,b"}`, `{"Id":"2","Name":"c"}`)
	var want bytes.Buffer
	writeCSV(&want, r, 2)
	var got, warn bytes.Buffer
	s := &csvSink{w: &got, warn: &warn}
	s.begin(2)
	s.write(r[:1])
	s.write(r[1:])
	s.end(2)
	if got.String() != want.String() {
		t.Errorf("got %q want %q", got.String(), want.String())
	}
	if warn.Len() != 0 {
		t.Errorf("unexpected warning %q", warn.String())
	}

	got.Reset()
	s = &csvSink{w: &got, warn: &warn}
	s.begin(2)
	s.write(recs(`{"Id":"1"}`))
	s.write(recs(`{"Id":"2","Extra":"x"}`))
	s.end(2)
	if got.String() != "Id\n1\n2\n" || !strings.Contains(warn.String(), "Extra") {
		t.Errorf("got %q warn %q", got.String(), warn.String())
	}

	got.Reset()
	s = &csvSink{w: &got, warn: &warn}
	s.begin(5)
	s.end(5)
	if got.String() != "count\n5\n" {
		t.Errorf("count: %q", got.String())
	}
}
