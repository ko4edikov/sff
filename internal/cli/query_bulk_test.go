package cli

import (
	"bytes"
	"strings"
	"testing"

	"github.com/parquet-go/parquet-go"
)

func TestCSVToParquet(t *testing.T) {
	csvIn := "\"Id\",\"IsDeleted\",\"N\",\"Owner.Name\"\n\"a\",\"false\",\"0\",\"Bob\"\n\"b\",\"true\",\"\",\"\"\n"
	var out bytes.Buffer
	kinds := map[string]pqKind{"isdeleted": pqBool, "n": pqInt}
	if err := csvToParquet(strings.NewReader(csvIn), &out, kinds); err != nil {
		t.Fatal(err)
	}
	f, err := parquet.OpenFile(bytes.NewReader(out.Bytes()), int64(out.Len()))
	if err != nil {
		t.Fatal(err)
	}
	if f.NumRows() != 2 {
		t.Fatalf("rows = %d, want 2", f.NumRows())
	}
	if got := len(f.Schema().Fields()); got != 4 {
		t.Fatalf("columns = %d, want 4", got)
	}
}
