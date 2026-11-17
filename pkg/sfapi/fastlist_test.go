package sfapi

import "testing"

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
