package cli

import (
	"strings"
	"testing"

	"github.com/pmezard/go-difflib/difflib"
)

func TestParseIgnore(t *testing.T) {
	cases := map[string]ignoreMode{
		"":                 ignoreNone,
		"none":             ignoreNone,
		"trim":             ignoreTrim,
		"whitespace":       ignoreWS,
		"ws":               ignoreWS,
		"whitespace-blank": ignoreWSBlank,
		"WS-Blank":         ignoreWSBlank,
	}
	for in, want := range cases {
		got, err := parseIgnore(in)
		if err != nil || got != want {
			t.Errorf("parseIgnore(%q) = %v, %v; want %v", in, got, err, want)
		}
	}
	if _, err := parseIgnore("bogus"); err == nil {
		t.Error("parseIgnore(bogus): expected error")
	}
}

// diffStr is a test helper that runs the built-in renderer over two strings.
func diffStr(a, b string, m ignoreMode) string {
	aOrig, aKey := prepLines(difflib.SplitLines(a), m)
	bOrig, bKey := prepLines(difflib.SplitLines(b), m)
	return unifiedDiff(aOrig, bOrig, aKey, bKey, "a", "", "b", "", 3)
}

func TestUnifiedDiffIgnoreModes(t *testing.T) {
	t.Run("whitespace-only change is shown by default", func(t *testing.T) {
		if diffStr("foo  bar\n", "foo bar\n", ignoreNone) == "" {
			t.Error("expected a diff with ignoreNone")
		}
	})
	t.Run("whitespace-only change is hidden by ignoreWS", func(t *testing.T) {
		if got := diffStr("foo  bar\n", "foo bar\n", ignoreWS); got != "" {
			t.Errorf("expected no diff with ignoreWS, got:\n%s", got)
		}
	})
	t.Run("trim hides trailing whitespace", func(t *testing.T) {
		if got := diffStr("foo \n", "foo\n", ignoreTrim); got != "" {
			t.Errorf("expected no diff with ignoreTrim, got:\n%s", got)
		}
	})
	t.Run("blank-line change hidden by ignoreWSBlank", func(t *testing.T) {
		if got := diffStr("x\n\ny\n", "x\ny\n", ignoreWSBlank); got != "" {
			t.Errorf("expected no diff with ignoreWSBlank, got:\n%s", got)
		}
	})
	t.Run("real change shown with original lines", func(t *testing.T) {
		got := diffStr("foo  bar\n", "foo  baz\n", ignoreWS)
		if !strings.Contains(got, "-foo  bar") || !strings.Contains(got, "+foo  baz") {
			t.Errorf("expected original lines in diff, got:\n%s", got)
		}
	})
}
