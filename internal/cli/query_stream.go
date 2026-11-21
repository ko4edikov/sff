package cli

import (
	"bufio"
	"bytes"
	"context"
	"encoding/csv"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/ko4edikov/sff/pkg/auth"
	"github.com/ko4edikov/sff/pkg/progress"
	"github.com/ko4edikov/sff/pkg/sfapi"
)

// recordSink receives query pages in record order and writes them out
// incrementally. begin is called once with the first page's totalSize.
type recordSink interface {
	begin(totalSize int) error
	write(records []json.RawMessage) error
	// end finishes the output (closing brackets, flush). Called only on success.
	end(totalSize int) error
}

// runQueryStream streams a query straight to the output (JSON/CSV) so memory
// stays flat and the first rows appear as soon as the first page arrives.
func runQueryStream(ctx context.Context, soql string, format outFormat, outFile string, useTooling bool, concurrency int) error {
	org, err := auth.Resolve(targetOrg)
	if err != nil {
		return err
	}
	client := sfapi.New(org)
	client.BatchConcurrency = concurrency

	var out io.Writer = os.Stdout
	if outFile != "" {
		f, err := os.Create(outFile)
		if err != nil {
			return fmt.Errorf("create %s: %w", outFile, err)
		}
		defer f.Close()
		out = f
	}
	bw := bufio.NewWriterSize(out, 64*1024)

	var sink recordSink
	if format == formatJSON {
		sink = &jsonSink{w: bw}
	} else {
		sink = &csvSink{w: bw, warn: os.Stderr}
	}

	prog := progress.Start("querying " + org.Username)
	start := time.Now()
	var count, total int
	first := true
	err = client.QueryStream(ctx, soql, useTooling, func(recs []json.RawMessage, totalSize int) error {
		total = totalSize
		if first {
			first = false
			prog.Stop() // before the first byte, so the spinner never interleaves with data
			if err := sink.begin(totalSize); err != nil {
				return err
			}
		}
		count += len(recs)
		return sink.write(recs)
	})
	prog.Stop()
	if err == nil {
		err = sink.end(total)
	}
	if ferr := bw.Flush(); err == nil {
		err = ferr
	}
	if err != nil {
		if count > 0 {
			err = fmt.Errorf("%w (output is incomplete: %d record(s) written)", err, count)
		}
		return err
	}

	elapsed := time.Since(start)
	if outFile != "" {
		fmt.Fprintf(os.Stderr, "wrote %d record(s) to %s in %s\n", count, outFile, fmtDuration(elapsed))
	} else {
		fmt.Fprintf(os.Stderr, "%d row(s) returned (totalSize %d) in %s\n", count, total, fmtDuration(elapsed))
	}
	return nil
}

// jsonSink streams the same document writeJSON produces (sf's
// `{status, result:{records,totalSize,done}}` shape, 2-space indent).
type jsonSink struct {
	w io.Writer
	n int
}

func (s *jsonSink) begin(int) error {
	_, err := io.WriteString(s.w, "{\n  \"status\": 0,\n  \"result\": {\n    \"records\": [")
	return err
}

func (s *jsonSink) write(records []json.RawMessage) error {
	var buf bytes.Buffer
	for _, r := range records {
		if s.n > 0 {
			buf.WriteByte(',')
		}
		buf.WriteString("\n      ")
		if err := json.Indent(&buf, r, "      ", "  "); err != nil {
			return err
		}
		s.n++
	}
	_, err := s.w.Write(buf.Bytes())
	return err
}

func (s *jsonSink) end(total int) error {
	tail := "]"
	if s.n > 0 {
		tail = "\n    ]"
	}
	_, err := fmt.Fprintf(s.w, "%s,\n    \"totalSize\": %d,\n    \"done\": true\n  }\n}\n", tail, total)
	return err
}

// csvSink writes CSV incrementally. The header is fixed by the first page
// (columns are derived from data, see sfapi.FlattenRecords), so a column that
// first appears on a later page (e.g. a parent relationship that was null on
// every earlier row, or another TYPEOF branch) cannot be added; such columns
// are dropped and reported once on warn.
type csvSink struct {
	w       io.Writer
	warn    io.Writer
	cw      *csv.Writer
	cols    []string
	known   map[string]bool
	dropped map[string]bool
	total   int
	wrote   bool
}

func (s *csvSink) begin(total int) error {
	s.cw = csv.NewWriter(s.w)
	s.total = total
	return nil
}

func (s *csvSink) write(records []json.RawMessage) error {
	if len(records) == 0 {
		return nil
	}
	cols, rows, err := sfapi.FlattenRecords(records)
	if err != nil {
		return err
	}
	if !s.wrote {
		s.wrote = true
		s.cols = cols
		s.known = make(map[string]bool, len(cols))
		for _, c := range cols {
			s.known[c] = true
		}
		if err := s.cw.Write(cols); err != nil {
			return err
		}
	} else {
		// Later pages may flatten to a different column set; check every cell key.
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
	line := make([]string, len(s.cols))
	for _, row := range rows {
		for i, c := range s.cols {
			line[i] = row[c]
		}
		if err := s.cw.Write(line); err != nil {
			return err
		}
	}
	s.cw.Flush()
	return s.cw.Error()
}

// isRedundant reports whether a column is the bare form of a relationship
// whose dotted children are already in the header (FlattenRecords drops those).
func (s *csvSink) isRedundant(c string) bool {
	for k := range s.known {
		if strings.HasPrefix(k, c+".") {
			return true
		}
	}
	return false
}

func (s *csvSink) end(total int) error {
	if !s.wrote && total > 0 { // COUNT() query: no records, only totalSize
		_ = s.cw.Write([]string{"count"})
		_ = s.cw.Write([]string{strconv.Itoa(total)})
	}
	s.cw.Flush()
	if len(s.dropped) > 0 {
		names := make([]string, 0, len(s.dropped))
		for c := range s.dropped {
			names = append(names, c)
		}
		sort.Strings(names)
		fmt.Fprintf(s.warn, "warning: column(s) not present in the first page were omitted from the CSV: %s\n", strings.Join(names, ", "))
	}
	return s.cw.Error()
}
