package cli

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/parquet-go/parquet-go"
	"github.com/parquet-go/parquet-go/compress/zstd"

	"github.com/ko4edikov/sff/pkg/sfapi"
)

// errNoRows is returned by parquetSink.end when the query produced nothing to
// describe a schema from; the caller removes the (empty) output file.
var errNoRows = errors.New("query returned no records; no Parquet file written")

// pqKind is the Parquet column type chosen for a query column.
type pqKind int

const (
	pqString pqKind = iota
	pqBool
	pqInt
	pqDouble
	pqDate
	pqTimestamp
)

// parquetSink streams query pages into a Parquet file, one row group per page.
//
// The schema is fixed by the first page: columns of the root object are typed
// from its describe (ints, doubles, dates, timestamps, booleans), dotted
// relationship columns (Owner.Name) from the JSON types seen on that page
// (all-null → string). Every column is optional. Like the CSV sink, columns
// that first appear on a later page are dropped with a warning; a value that
// does not fit its column type is written as null and counted.
type parquetSink struct {
	w        io.Writer
	warn     io.Writer
	describe func(sobject string) (*sfapi.SObjectDescribe, error)

	pw      *parquet.GenericWriter[map[string]any]
	cols    []string
	kinds   map[string]pqKind
	known   map[string]bool
	dropped map[string]bool
	badVals map[string]int
	total   int
}

func (s *parquetSink) begin(total int) error {
	s.total = total
	return nil
}

func (s *parquetSink) write(records []json.RawMessage) error {
	if len(records) == 0 {
		return nil
	}
	cols, rows, jsonKinds, err := sfapi.FlattenRecordsKinds(records)
	if err != nil {
		return err
	}
	if s.pw == nil {
		if err := s.init(records[0], cols, jsonKinds); err != nil {
			return err
		}
	} else {
		for _, row := range rows {
			for c := range row {
				if !s.known[c] && !s.isRedundant(c) {
					if s.dropped == nil {
						s.dropped = map[string]bool{}
					}
					s.dropped[c] = true
				}
			}
		}
	}
	out := make([]map[string]any, len(rows))
	for i, row := range rows {
		m := make(map[string]any, len(s.cols))
		for _, c := range s.cols {
			v, ok := row[c]
			if !ok || v == "" {
				continue // null
			}
			cv, err := convertCell(s.kinds[c], v)
			if err != nil {
				if s.badVals == nil {
					s.badVals = map[string]int{}
				}
				s.badVals[c]++
				continue
			}
			m[c] = cv
		}
		out[i] = m
	}
	if _, err := s.pw.Write(out); err != nil {
		return err
	}
	return s.pw.Flush() // one row group per page
}

// init builds the schema and the writer from the first page.
func (s *parquetSink) init(first json.RawMessage, cols []string, jsonKinds map[string]sfapi.ValueKind) error {
	described := s.describeRoot(first)
	s.cols = cols
	s.kinds = make(map[string]pqKind, len(cols))
	s.known = make(map[string]bool, len(cols))
	group := parquet.Group{}
	for _, c := range cols {
		k, ok := described[c]
		if !ok {
			k = kindFromJSON(jsonKinds[c])
		}
		s.kinds[c] = k
		s.known[c] = true
		group[c] = parquet.Optional(k.node())
	}
	return s.open(group)
}

func (s *parquetSink) open(group parquet.Group) error {
	schema := parquet.NewSchema("query", group)
	s.pw = parquet.NewGenericWriter[map[string]any](s.w, schema, parquet.Compression(&zstd.Codec{}))
	return nil
}

// describeRoot types the root object's own (non-dotted) columns from its describe.
func (s *parquetSink) describeRoot(first json.RawMessage) map[string]pqKind {
	var rec struct {
		Attributes struct {
			Type string `json:"type"`
		} `json:"attributes"`
	}
	if json.Unmarshal(first, &rec) != nil || rec.Attributes.Type == "" || s.describe == nil {
		return nil
	}
	d, err := s.describe(rec.Attributes.Type)
	if err != nil || d == nil {
		return nil
	}
	out := make(map[string]pqKind, len(d.Fields))
	for _, f := range d.Fields {
		out[f.Name] = kindFromDescribe(f.Type)
	}
	return out
}

func kindFromDescribe(t string) pqKind {
	switch t {
	case "boolean":
		return pqBool
	case "int", "long":
		return pqInt
	case "double", "currency", "percent":
		return pqDouble
	case "date":
		return pqDate
	case "datetime":
		return pqTimestamp
	}
	return pqString
}

func kindFromJSON(k sfapi.ValueKind) pqKind {
	switch k {
	case sfapi.KindBool:
		return pqBool
	case sfapi.KindNumber:
		return pqDouble
	}
	return pqString
}

func (k pqKind) node() parquet.Node {
	switch k {
	case pqBool:
		return parquet.Leaf(parquet.BooleanType)
	case pqInt:
		return parquet.Int(64)
	case pqDouble:
		return parquet.Leaf(parquet.DoubleType)
	case pqDate:
		return parquet.Date()
	case pqTimestamp:
		return parquet.Timestamp(parquet.Microsecond)
	}
	return parquet.String()
}

// convertCell converts a flattened cell string to the Go value parquet-go
// expects for the column kind (DATE = days since epoch, TIMESTAMP = µs, UTC).
func convertCell(k pqKind, v string) (any, error) {
	switch k {
	case pqBool:
		return strconv.ParseBool(v)
	case pqInt:
		if n, err := strconv.ParseInt(v, 10, 64); err == nil {
			return n, nil
		}
		f, err := strconv.ParseFloat(v, 64) // e.g. "5.0"
		if err != nil || f != float64(int64(f)) {
			return nil, fmt.Errorf("not an integer: %q", v)
		}
		return int64(f), nil
	case pqDouble:
		return strconv.ParseFloat(v, 64)
	case pqDate:
		t, err := time.Parse("2006-01-02", v)
		if err != nil {
			return nil, err
		}
		return int32(t.Unix() / 86400), nil
	case pqTimestamp:
		for _, layout := range []string{"2006-01-02T15:04:05.000-0700", "2006-01-02T15:04:05-0700", time.RFC3339Nano} {
			if t, err := time.Parse(layout, v); err == nil {
				return t.UnixMicro(), nil
			}
		}
		return nil, fmt.Errorf("bad datetime: %q", v)
	}
	return v, nil
}

// isRedundant reports whether c is the bare form of a relationship whose
// dotted children are already columns (FlattenRecords drops those).
func (s *parquetSink) isRedundant(c string) bool {
	for k := range s.known {
		if strings.HasPrefix(k, c+".") {
			return true
		}
	}
	return false
}

func (s *parquetSink) end(total int) error {
	if s.pw == nil {
		if total == 0 {
			return errNoRows
		}
		// COUNT() query: no records, only totalSize.
		if err := s.open(parquet.Group{"count": parquet.Optional(parquet.Int(64))}); err != nil {
			return err
		}
		if _, err := s.pw.Write([]map[string]any{{"count": int64(total)}}); err != nil {
			return err
		}
	}
	if err := s.pw.Close(); err != nil {
		return err
	}
	if len(s.dropped) > 0 {
		fmt.Fprintf(s.warn, "warning: column(s) not present in the first page were omitted from the Parquet file: %s (run without --stream to keep them)\n", joinSorted(s.dropped))
	}
	for c, n := range s.badVals {
		fmt.Fprintf(s.warn, "warning: %d value(s) of column %s did not match its Parquet type and were written as null\n", n, c)
	}
	return nil
}

func joinSorted(m map[string]bool) string {
	names := make([]string, 0, len(m))
	for c := range m {
		names = append(names, c)
	}
	sort.Strings(names)
	return strings.Join(names, ", ")
}

// pqChunk is how many records a buffered Parquet export converts per row group.
const pqChunk = 50000

// writeParquet writes an already-fetched result as Parquet. The schema is
// built from every record (no first-page limit), then rows are written in
// row groups of pqChunk records.
func writeParquet(w io.Writer, records []json.RawMessage, total int, describe func(string) (*sfapi.SObjectDescribe, error)) error {
	bw := bufio.NewWriterSize(w, 64*1024)
	s := &parquetSink{w: bw, warn: os.Stderr, describe: describe}
	if err := s.begin(total); err != nil {
		return err
	}
	if len(records) > 0 {
		cols, _, kinds, err := sfapi.FlattenRecordsKinds(records)
		if err != nil {
			return err
		}
		if err := s.init(records[0], cols, kinds); err != nil {
			return err
		}
	}
	for i := 0; i < len(records); i += pqChunk {
		if err := s.write(records[i:min(i+pqChunk, len(records))]); err != nil {
			return err
		}
	}
	if err := s.end(total); err != nil {
		return err
	}
	return bw.Flush()
}
