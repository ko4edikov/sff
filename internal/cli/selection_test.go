package cli

import (
	"testing"
	"time"

	"github.com/ko4edikov/sff/pkg/mdapi"
)

func TestParseWhen(t *testing.T) {
	now := time.Date(2026, 11, 17, 15, 30, 0, 0, time.UTC)
	cases := map[string]time.Time{
		"2h":               now.Add(-2 * time.Hour),
		"30m":              now.Add(-30 * time.Minute),
		"3d":               now.AddDate(0, 0, -3),
		"3 days ago":       now.AddDate(0, 0, -3),
		"1w":               now.AddDate(0, 0, -7),
		"today":            time.Date(2026, 11, 17, 0, 0, 0, 0, time.UTC),
		"yesterday":        time.Date(2026, 11, 16, 0, 0, 0, 0, time.UTC),
		"2026-11-01":       time.Date(2026, 11, 1, 0, 0, 0, 0, time.UTC),
		"2026-11-01T09:00": time.Date(2026, 11, 1, 9, 0, 0, 0, time.UTC),
	}
	for in, want := range cases {
		got, err := parseWhen(in, now)
		if err != nil || !got.Equal(want) {
			t.Errorf("parseWhen(%q) = %v, %v; want %v", in, got, err, want)
		}
	}
	if _, err := parseWhen("soon", now); err == nil {
		t.Error("expected error for invalid time")
	}
}

func TestLooksManaged(t *testing.T) {
	managed := []string{"clm__Foo", "clm__Foo__c", "Account.clm__Field__c", "clm__Obj__c.EUR_F__c"}
	own := []string{"EUR_Foo", "EUR_Obj__c", "Account.EUR_Field__c", "Foo__c", "Obj__mdt", "Account-EUR AT Layout"}
	for _, n := range managed {
		if !looksManaged(n) {
			t.Errorf("looksManaged(%q) = false, want true", n)
		}
	}
	for _, n := range own {
		if looksManaged(n) {
			t.Errorf("looksManaged(%q) = true, want false", n)
		}
	}
}

func TestNormalizeSourceMember(t *testing.T) {
	cases := []struct{ typ, name, wantTyp, wantName string }{
		{"LightningComponentResource", "myCmp/myCmp.html", "LightningComponentBundle", "myCmp"},
		{"AuraDefinition", "MyAura/MyAura.cmp", "AuraDefinitionBundle", "MyAura"},
		{"ExperienceResource", "Site1/config/a.json", "ExperienceBundle", "Site1"},
		{"ApexClass", "Foo", "ApexClass", "Foo"},
		{"CustomField", "Account.X__c", "CustomField", "Account.X__c"},
	}
	for _, c := range cases {
		typ, name, ok := normalizeSourceMember(c.typ, c.name)
		if !ok || typ != c.wantTyp || name != c.wantName {
			t.Errorf("normalize(%s,%s) = %s,%s,%v", c.typ, c.name, typ, name, ok)
		}
	}
}

func TestScopeMatcher(t *testing.T) {
	all := scopeMatcher(nil)
	if !all("ApexClass", "X") {
		t.Error("nil scope should select everything")
	}
	m := scopeMatcher(&mdapi.Package{Types: []mdapi.PackageTypes{
		{Name: "ApexClass", Members: []string{"EUR_*"}},
		{Name: "CustomField", Members: []string{"*"}},
		{Name: "Layout", Members: []string{"Account-Main"}},
	}})
	for _, c := range []struct {
		typ, name string
		want      bool
	}{
		{"ApexClass", "eur_Foo", true}, {"ApexClass", "Other", false},
		{"CustomField", "Account.X__c", true}, {"Layout", "account-main", true},
		{"Layout", "Account-Other", false}, {"Flow", "X", false},
	} {
		if got := m(c.typ, c.name); got != c.want {
			t.Errorf("match(%s,%s) = %v, want %v", c.typ, c.name, got, c.want)
		}
	}
}

func TestApplyExclusionsProtectsExplicit(t *testing.T) {
	entries := []modEntry{
		{Type: "PermissionSet", Name: "EUR_A"}, {Type: "PermissionSet", Name: "Other"},
		{Type: "ApexClass", Name: "Foo"},
	}
	excl := scopeMatcher(&mdapi.Package{Types: []mdapi.PackageTypes{{Name: "PermissionSet", Members: []string{"*"}}}})

	// -e permissionset alone drops both permission sets.
	got := applyExclusions(entries, excl, explicitMatcher(nil))
	if len(got) != 1 || got[0].Name != "Foo" {
		t.Errorf("exclude all permsets: %v", got)
	}
	// An exact name or a pattern in -m is protected; a bare type is not.
	for _, m := range []string{"EUR_A", "EUR_*"} {
		scope := &mdapi.Package{Types: []mdapi.PackageTypes{{Name: "PermissionSet", Members: []string{m}}}}
		got = applyExclusions(entries, excl, explicitMatcher(scope))
		if len(got) != 2 || got[0].Name != "EUR_A" {
			t.Errorf("-m permissionset:%s should protect EUR_A, got %v", m, got)
		}
	}
	bare := &mdapi.Package{Types: []mdapi.PackageTypes{{Name: "PermissionSet", Members: []string{"*"}}}}
	if got = applyExclusions(entries, excl, explicitMatcher(bare)); len(got) != 1 {
		t.Errorf("bare -m type must not protect: %v", got)
	}
}

func TestHideCascades(t *testing.T) {
	base := time.Date(2026, 11, 17, 18, 42, 0, 0, time.UTC)
	var entries []modEntry
	for i := 0; i < 25; i++ { // a burst: 25 permsets within 25s by one user
		entries = append(entries, modEntry{Type: "PermissionSet", Name: "PS" + string(rune('A'+i)), ModifiedBy: "Me", Modified: base.Add(time.Duration(i) * time.Second)})
	}
	entries = append(entries,
		modEntry{Type: "PermissionSet", Name: "Solo", ModifiedBy: "Me", Modified: base.Add(2 * time.Hour)},
		modEntry{Type: "ApexClass", Name: "Foo", ModifiedBy: "Me", Modified: base},
	)
	kept, hidden := hideCascades(entries, map[string]bool{})
	if hidden["PermissionSet"] != 25 || len(kept) != 2 {
		t.Errorf("hidden=%v kept=%v", hidden, kept)
	}
	// Explicitly scoped type: nothing is hidden.
	kept, hidden = hideCascades(entries, map[string]bool{"permissionset": true})
	if hidden != nil || len(kept) != len(entries) {
		t.Errorf("explicit type must not be hidden: hidden=%v kept=%d", hidden, len(kept))
	}
	// A small group (below the threshold) is kept.
	kept, hidden = hideCascades(entries[:5], map[string]bool{})
	if hidden != nil || len(kept) != 5 {
		t.Errorf("small group hidden: %v", hidden)
	}
}
