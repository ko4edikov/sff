package sfapi

import (
	"encoding/json"
	"reflect"
	"testing"
)

func TestFlattenRecords(t *testing.T) {
	recs := []json.RawMessage{
		json.RawMessage(`{"attributes":{"type":"X"},"Territory2":{"attributes":{},"Ext":"A"},"Object":{"attributes":{"type":"Account"},"P":"p1","T":"t1"},"Kids":{"totalSize":1,"done":true,"records":[{"Id":"1"}]}}`),
		json.RawMessage(`{"attributes":{"type":"X"},"Territory2":null,"Object":{"attributes":{"type":"Contact"},"Email":"e@x"},"Kids":null}`),
	}
	cols, rows, err := FlattenRecords(recs)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"Territory2.Ext", "Object.P", "Object.T", "Kids", "Object.Email"}
	if !reflect.DeepEqual(cols, want) {
		t.Fatalf("cols = %v, want %v", cols, want)
	}
	if rows[0]["Object.P"] != "p1" || rows[1]["Object.Email"] != "e@x" || rows[1]["Object.P"] != "" {
		t.Fatalf("rows = %v", rows)
	}
	if rows[0]["Kids"] != `{"totalSize":1,"done":true,"records":[{"Id":"1"}]}` {
		t.Fatalf("kids = %q", rows[0]["Kids"])
	}
}
