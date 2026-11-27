package cli

import (
	"bufio"
	"context"
	"encoding/csv"
	"fmt"
	"io"
	"os"
	"os/signal"
	"regexp"
	"strings"
	"syscall"
	"time"

	"github.com/parquet-go/parquet-go"
	"github.com/parquet-go/parquet-go/compress/zstd"

	"github.com/ko4edikov/sff/pkg/auth"
	"github.com/ko4edikov/sff/pkg/progress"
	"github.com/ko4edikov/sff/pkg/sfapi"
)

// runBulkQuery runs soql as a Bulk API 2.0 query job and streams the CSV result
// to outFile (or stdout). Ctrl+C aborts the job server-side.
func runBulkQuery(ctx context.Context, soql, outFile string, concurrency int, parquetOut, allRows bool) error {
	org, err := auth.Resolve(targetOrg)
	if err != nil {
		return err
	}
	ctx, stop := signal.NotifyContext(ctx, os.Interrupt, syscall.SIGTERM)
	defer stop()

	client := sfapi.New(org)
	client.AllRows = allRows

	var out io.Writer = os.Stdout
	if outFile != "" {
		f, err := os.Create(outFile)
		if err != nil {
			return fmt.Errorf("create %s: %w", outFile, err)
		}
		defer f.Close()
		out = f
	}
	bw := bufio.NewWriterSize(out, 256*1024)

	prog := progress.Start("bulk query " + org.Username)
	stopped := false
	stopProg := func() {
		if !stopped {
			stopped = true
			prog.Stop()
		}
	}
	defer stopProg()

	start := time.Now()
	opts := sfapi.BulkQueryOptions{
		Concurrency: concurrency,
		OnStatus: func(s sfapi.BulkQueryStatus) {
			prog.Update(fmt.Sprintf("bulk query %s: %s, %d row(s) processed", s.ID, s.State, s.NumberRecords))
		},
		OnChunk: func(n int64) {
			prog.Update(fmt.Sprintf("downloading: %d row(s) written", n))
		},
	}
	var rows int64
	if parquetOut {
		rows, err = bulkToParquet(ctx, client, soql, bw, opts)
	} else {
		rows, err = client.BulkQuery(ctx, soql, bw, opts)
	}
	if ferr := bw.Flush(); err == nil {
		err = ferr
	}
	stopProg()
	if err != nil {
		return err
	}
	if outFile != "" {
		fmt.Fprintf(os.Stderr, "wrote %d record(s) to %s in %s\n", rows, outFile, fmtDuration(time.Since(start)))
	} else {
		fmt.Fprintf(os.Stderr, "%d row(s) in %s\n", rows, fmtDuration(time.Since(start)))
	}
	return nil
}

// bulkToParquet runs the bulk job and converts its CSV result to Parquet on the
// fly (one row group per pqChunk rows), so neither the CSV nor the whole result
// is ever held in memory or on disk beyond the in-flight download chunks.
func bulkToParquet(ctx context.Context, client *sfapi.Client, soql string, w io.Writer, opts sfapi.BulkQueryOptions) (int64, error) {
	pr, pw := io.Pipe()
	type result struct {
		rows int64
		err  error
	}
	done := make(chan result, 1)
	go func() {
		n, err := client.BulkQuery(ctx, soql, pw, opts)
		pw.CloseWithError(err) // nil err = clean EOF
		done <- result{n, err}
	}()
	perr := csvToParquet(pr, w, bulkKinds(ctx, client, soql))
	pr.CloseWithError(perr) // unblock the producer if we stopped early
	res := <-done
	if res.err != nil {
		return res.rows, res.err
	}
	return res.rows, perr
}

var fromRe = regexp.MustCompile(`(?i)\bfrom\s+([A-Za-z0-9_]+)`)

// bulkKinds types the root object's columns from its describe; nil (all
// strings) when the object can't be determined or described.
func bulkKinds(ctx context.Context, client *sfapi.Client, soql string) map[string]pqKind {
	m := fromRe.FindStringSubmatch(soql)
	if m == nil {
		return nil
	}
	d, err := client.Describe(ctx, m[1], false)
	if err != nil || d == nil {
		return nil
	}
	out := make(map[string]pqKind, len(d.Fields))
	for _, f := range d.Fields {
		out[strings.ToLower(f.Name)] = kindFromDescribe(f.Type)
	}
	return out
}

// csvToParquet reads Bulk CSV (header + rows; empty cell = null) from r and
// writes Parquet to w. Columns present in kinds (lower-cased field names) are
// typed; everything else (relationship columns, unknown fields) is a string.
func csvToParquet(r io.Reader, w io.Writer, kinds map[string]pqKind) error {
	cr := csv.NewReader(bufio.NewReaderSize(r, 256*1024))
	cr.ReuseRecord = true
	header, err := cr.Read()
	if err == io.EOF {
		return nil
	}
	if err != nil {
		return err
	}
	cols := append([]string(nil), header...)
	ck := make([]pqKind, len(cols))
	group := parquet.Group{}
	for i, c := range cols {
		ck[i] = kinds[strings.ToLower(c)] // zero value = pqString
		group[c] = parquet.Optional(ck[i].node())
	}
	pw := parquet.NewGenericWriter[map[string]any](w, parquet.NewSchema("query", group), parquet.Compression(&zstd.Codec{}))

	bad := map[string]int{}
	batch := make([]map[string]any, 0, pqChunk)
	flush := func() error {
		if len(batch) == 0 {
			return nil
		}
		if _, err := pw.Write(batch); err != nil {
			return err
		}
		batch = batch[:0]
		return pw.Flush() // one row group per chunk
	}
	for {
		rec, err := cr.Read()
		if err == io.EOF {
			break
		}
		if err != nil {
			return err
		}
		m := make(map[string]any, len(cols))
		for i, v := range rec {
			if v == "" {
				continue
			}
			cv, err := convertCell(ck[i], v)
			if err != nil {
				bad[cols[i]]++
				continue
			}
			m[cols[i]] = boxCell(cv)
		}
		batch = append(batch, m)
		if len(batch) == pqChunk {
			if err := flush(); err != nil {
				return err
			}
		}
	}
	if err := flush(); err != nil {
		return err
	}
	if err := pw.Close(); err != nil {
		return err
	}
	for c, n := range bad {
		fmt.Fprintf(os.Stderr, "warning: %d value(s) of column %s did not match its Parquet type and were written as null\n", n, c)
	}
	return nil
}
