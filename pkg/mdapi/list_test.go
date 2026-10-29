package mdapi

import "testing"

func TestIsGlob(t *testing.T) {
	cases := map[string]bool{"*": false, "Foo": false, "Foo*": true, "*Foo": true, "A?b": true}
	for in, want := range cases {
		if got := IsGlob(in); got != want {
			t.Errorf("IsGlob(%q) = %v, want %v", in, got, want)
		}
	}
}

func TestGlobRegexp(t *testing.T) {
	re := globRegexp("EUR_Score*")
	for name, want := range map[string]bool{
		"EUR_Score__c": true, "eur_score_a__c": true, "EUR_Score": true,
		"XEUR_Score__c": false, "EUR_Scor": false,
	} {
		if got := re.MatchString(name); got != want {
			t.Errorf("match %q = %v, want %v", name, got, want)
		}
	}
	if !globRegexp("A.b?").MatchString("A.bX") || globRegexp("A.b?").MatchString("AXbX") {
		t.Error("regexp metacharacters in the pattern must be literal")
	}
}
