package cli

import (
	"context"
	"sort"
	"testing"

	"os"

	"github.com/ko4edikov/sff/pkg/auth"
	"github.com/ko4edikov/sff/pkg/mdapi"
)

// TestFastListMatchesListMetadata compares the SOQL fast path with listMetadata
// for every covered type against a real org. Opt in with SFF_LIVE_ORG=<alias>.
func TestFastListMatchesListMetadata(t *testing.T) {
	alias := os.Getenv("SFF_LIVE_ORG")
	if alias == "" {
		t.Skip("set SFF_LIVE_ORG to compare against a live org")
	}
	org, err := auth.Resolve(alias)
	if err != nil {
		t.Fatal(err)
	}
	c := newMDClient(org)
	c.APIVersion = "68.0"
	ctx := context.Background()
	for _, typ := range []string{"ApexClass", "ApexTrigger", "ApexPage", "ApexComponent", "StaticResource",
		"AuraDefinitionBundle", "LightningComponentBundle", "RecordType", "ValidationRule", "CustomField", "CustomObject"} {
		t.Run(typ, func(t *testing.T) {
			fast, handled, err := c.FastList(ctx, typ, nil)
			if !handled {
				t.Skip("no fast path")
			}
			if err != nil {
				t.Fatal(err)
			}
			slow, err := c.ListMetadata(ctx, []mdapi.ListQuery{{Type: typ}})
			if err != nil {
				t.Fatal(err)
			}
			f := map[string]bool{}
			for _, n := range fast {
				f[n] = true
			}
			s := map[string]bool{}
			for _, p := range slow {
				s[p.FullName] = true
			}
			var onlyFast, onlySlow []string
			for n := range f {
				if !s[n] {
					onlyFast = append(onlyFast, n)
				}
			}
			for n := range s {
				if !f[n] {
					onlySlow = append(onlySlow, n)
				}
			}
			sort.Strings(onlyFast)
			sort.Strings(onlySlow)
			cut := func(x []string) []string { return x[:min(len(x), 8)] }
			t.Logf("%s: fast=%d listMetadata=%d onlyFast=%d %v onlySlow=%d %v", typ, len(f), len(s), len(onlyFast), cut(onlyFast), len(onlySlow), cut(onlySlow))
		})
	}
}
