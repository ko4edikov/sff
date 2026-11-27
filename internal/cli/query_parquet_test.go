package cli

import (
	"bytes"
	"encoding/json"
	"io"
	"reflect"
	"strings"
	"testing"

	"github.com/parquet-go/parquet-go"

	"github.com/ko4edikov/sff/pkg/sfapi"
)

func raws(ss ...string) []json.RawMessage {
	out := make([]json.RawMessage, len(ss))
	for i, s := range ss {
		out[i] = json.RawMessage(s)
	}
	return out
}

func stubDescribe(string) (*sfapi.SObjectDescribe, error) {
	return &sfapi.SObjectDescribe{Fields: []sfapi.FieldDescribe{
		{Name: "Id", Type: "id"}, {Name: "Name", Type: "string"},
		{Name: "NumberOfEmployees", Type: "int"}, {Name: "AnnualRevenue", Type: "currency"},
		{Name: "IsActive", Type: "boolean"}, {Name: "Since", Type: "date"},
		{Name: "CreatedDate", Type: "datetime"},
	}}, nil
}

func TestParquetSink(t *testing.T) {
	var buf, warn bytes.Buffer
	s := &parquetSink{w: &buf, warn: &warn, describe: stubDescribe}
	if err := s.begin(3); err != nil {
		t.Fatal(err)
	}
	page1 := raws(
		`{"attributes":{"type":"Account"},"Id":"001A","Name":"Acme","NumberOfEmployees":5,"AnnualRevenue":1.5,"IsActive":true,"Since":"2020-01-02","CreatedDate":"2024-03-04T10:20:30.000+0000","Owner":{"attributes":{"type":"User"},"Name":"Ivan"},"Parent":null}`,
		`{"attributes":{"type":"Account"},"Id":"001B","Name":null,"NumberOfEmployees":null,"AnnualRevenue":null,"IsActive":false,"Since":null,"CreatedDate":"2024-03-04T10:20:30.000+0000","Owner":{"attributes":{"type":"User"},"Name":"Olga"},"Parent":null}`,
	)
	page2 := raws(`{"attributes":{"type":"Account"},"Id":"001C","Name":"Z","NumberOfEmployees":7,"AnnualRevenue":2,"IsActive":true,"Since":"1969-12-31","CreatedDate":"2024-03-04T10:20:30.000+0000","Owner":{"attributes":{"type":"User"},"Name":"X"},"Parent":{"attributes":{"type":"Account"},"Name":"P"}}`)
	if err := s.write(page1); err != nil {
		t.Fatal(err)
	}
	if err := s.write(page2); err != nil {
		t.Fatal(err)
	}
	if err := s.end(3); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(warn.String(), "Parent.Name") {
		t.Errorf("expected dropped-column warning, got %q", warn.String())
	}

	rd := bytes.NewReader(buf.Bytes())
	f, err := parquet.OpenFile(rd, int64(buf.Len()))
	if err != nil {
		t.Fatal(err)
	}
	if n := len(f.RowGroups()); n != 2 {
		t.Errorf("row groups = %d, want 2 (one per page)", n)
	}
	got := map[string]string{}
	for _, fld := range f.Schema().Fields() {
		got[fld.Name()] = fld.Type().String()
	}
	want := map[string]string{"NumberOfEmployees": "INT(64", "AnnualRevenue": "DOUBLE", "IsActive": "BOOLEAN", "Since": "DATE", "Name": "STRING", "Owner.Name": "STRING"}
	for k, v := range want {
		if !strings.Contains(got[k], v) {
			t.Errorf("column %s type = %q, want %s", k, got[k], v)
		}
	}
	if _, ok := got["Parent.Name"]; ok {
		t.Errorf("late column Parent.Name should be omitted")
	}

	pr := parquet.NewGenericReader[map[string]any](rd, f.Schema())
	rows := make([]map[string]any, 3)
	for i := range rows {
		rows[i] = map[string]any{}
	}
	n, err := pr.Read(rows)
	if err != nil && err != io.EOF {
		t.Fatal(err)
	}
	if n != 3 {
		t.Fatalf("read %d rows", n)
	}
	if rows[0]["NumberOfEmployees"] != int64(5) || rows[0]["Name"] != "Acme" {
		t.Errorf("row0 = %v", rows[0])
	}
	if rows[1]["Name"] != nil || rows[1]["NumberOfEmployees"] != nil {
		t.Errorf("row1 nulls = %v", rows[1])
	}
}

func TestParquetSinkCountAndEmpty(t *testing.T) {
	var buf bytes.Buffer
	s := &parquetSink{w: &buf, warn: io.Discard}
	if err := s.end(42); err != nil {
		t.Fatal(err)
	}
	if _, err := parquet.OpenFile(bytes.NewReader(buf.Bytes()), int64(buf.Len())); err != nil {
		t.Fatal(err)
	}
	if err := (&parquetSink{w: io.Discard, warn: io.Discard}).end(0); err != errNoRows {
		t.Errorf("empty result: got %v, want errNoRows", err)
	}
}

func TestConvertCell(t *testing.T) {
	if v, _ := convertCell(pqDate, "1970-01-02"); v != int32(1) {
		t.Errorf("date = %v", v)
	}
	if v, _ := convertCell(pqTimestamp, "1970-01-01T00:00:01.000+0000"); v != int64(1_000_000) {
		t.Errorf("ts = %v", v)
	}
	if v, _ := convertCell(pqInt, "5.0"); v != int64(5) {
		t.Errorf("int = %v", v)
	}
	if _, err := convertCell(pqInt, "5.5"); err == nil {
		t.Error("5.5 should not be an int")
	}
}

// Zero values (false, 0, 0.0) must survive as values, not become NULL.
func TestBoxCellKeepsZeroValues(t *testing.T) {
	for _, v := range []any{false, int64(0), 0.0, int32(0), ""} {
		b := boxCell(v)
		if b == nil || reflect.ValueOf(b).Kind() != reflect.Ptr || reflect.ValueOf(b).IsNil() {
			t.Fatalf("boxCell(%v) = %v, want non-nil pointer", v, b)
		}
		if reflect.ValueOf(b).Elem().Interface() != v {
			t.Fatalf("boxCell(%v) lost its value: %v", v, b)
		}
	}
}
