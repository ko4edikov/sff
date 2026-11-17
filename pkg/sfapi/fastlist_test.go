package sfapi

import (
	"testing"
	"time"
)

func TestCustomFieldWhere(t *testing.T) {
	cases := []struct {
		patterns []string
		want     string
	}{
		{[]string{"*.EUR_DeltaAverageScore__c"}, "DeveloperName LIKE 'EUR_DeltaAverageScore%'"},
		{[]string{"Account.EUR_*"}, "DeveloperName LIKE 'EUR_%'"},
		{[]string{"*.A_?x__c", "*.B*"}, "DeveloperName LIKE 'A__x%' OR DeveloperName LIKE 'B%'"},
		{[]string{"Account.*"}, ""},    // unconstrained field part
		{[]string{"*.*Score__c"}, ""},  // leading wildcard
		{[]string{"Acc*"}, ""},         // no object/field split
		{[]string{"*.ns__Fld__c"}, ""}, // namespaced
	}
	for _, c := range cases {
		got := customFieldWhere(c.patterns)
		if got != c.want {
			t.Errorf("customFieldWhere(%v) = %q, want %q", c.patterns, got, c.want)
		}
	}
}

func TestFastQueryWhere(t *testing.T) {
	if got := (FastQuery{}).where(); len(got) != 0 {
		t.Errorf("empty query should have no conditions, got %v", got)
	}
	since := time.Date(2026, 11, 1, 9, 0, 0, 0, time.UTC)
	got := FastQuery{Since: since, UserIDs: []string{"0057R00000CUlDKQA1"}}.where()
	want := []string{
		"LastModifiedDate >= 2026-11-01T09:00:00Z",
		"LastModifiedById IN ('0057R00000CUlDKQA1','0057R00000CUlDK')",
	}
	if len(got) != 2 || got[0] != want[0] || got[1] != want[1] {
		t.Errorf("where = %v, want %v", got, want)
	}
}
