package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"text/tabwriter"
	"time"

	"github.com/spf13/cobra"

	"github.com/ko4edikov/sff/pkg/auth"
	"github.com/ko4edikov/sff/pkg/mdapi"
	"github.com/ko4edikov/sff/pkg/sfapi"
)

// modFilter narrows a metadata selection by who changed it and when. It is
// shared by `sff retrieve` and `sff metadata list` so the two stay in step.
type modFilter struct {
	since, until     string
	modifiedBy       []string
	includePackages  bool
	exclude          []string
	includeCascading bool
}

// hasChangeFilter reports whether a time or author filter is set — the only
// filters that make the org's change tracking (SourceMember) relevant.
func (f *modFilter) hasChangeFilter() bool {
	return f.since != "" || f.until != "" || len(f.modifiedBy) > 0
}

// active reports whether the selection must go through resolveModified rather
// than plain -m expansion: any change filter, or an exclusion.
func (f *modFilter) active() bool {
	return f.hasChangeFilter() || len(f.exclude) > 0
}

// addModFilterFlags registers --since/--until/--modified-by/--include-packages.
func addModFilterFlags(cmd *cobra.Command, f *modFilter) {
	cmd.Flags().StringVar(&f.since, "since", "", "only components modified since: 30m, 2h, 3d, 1w, today, yesterday or a date (2026-11-01, 2026-11-01T09:00)")
	cmd.Flags().StringVar(&f.until, "until", "", "only components modified up to this time (same formats as --since)")
	cmd.Flags().StringSliceVar(&f.modifiedBy, "modified-by", nil, "only components last modified by: me, a username, a user Id (005…) or a name with * ? wildcards (repeatable, comma-separated)")
	cmd.Flags().StringArrayVarP(&f.exclude, "exclude-metadata", "e", nil, "drop components from the selection: Type, Type:Name or Type:pattern* (repeatable). Components named explicitly by -m (a name or pattern) are kept")
	cmd.Flags().BoolVar(&f.includeCascading, "include-cascading", false, "keep PermissionSet/PermissionSetGroup/Profile bursts that look like a side effect of another change (hidden by default)")
	cmd.Flags().BoolVar(&f.includePackages, "include-packages", false, "include components from installed (managed) packages, excluded by default")
}

var whenRelRe = regexp.MustCompile(`(?i)^(\d+)\s*(minutes?|mins?|m|hours?|h|days?|d|weeks?|w)(\s+ago)?$`)

// parseWhen parses a --since/--until value relative to now: "2h", "3 days ago",
// "today", "yesterday", or an absolute date/time (local time unless it carries
// a zone).
func parseWhen(s string, now time.Time) (time.Time, error) {
	s = strings.TrimSpace(s)
	midnight := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, now.Location())
	switch strings.ToLower(s) {
	case "today":
		return midnight, nil
	case "yesterday":
		return midnight.AddDate(0, 0, -1), nil
	}
	if m := whenRelRe.FindStringSubmatch(s); m != nil {
		n, _ := strconv.Atoi(m[1])
		switch strings.ToLower(m[2])[0] {
		case 'm':
			return now.Add(-time.Duration(n) * time.Minute), nil
		case 'h':
			return now.Add(-time.Duration(n) * time.Hour), nil
		case 'd':
			return now.AddDate(0, 0, -n), nil
		case 'w':
			return now.AddDate(0, 0, -7*n), nil
		}
	}
	if t, err := time.Parse(time.RFC3339, s); err == nil {
		return t, nil
	}
	for _, layout := range []string{"2006-01-02T15:04", "2006-01-02 15:04", "2006-01-02"} {
		if t, err := time.ParseInLocation(layout, s, now.Location()); err == nil {
			return t, nil
		}
	}
	return time.Time{}, fmt.Errorf("invalid time %q (use e.g. 2h, 3d, 1w, today, yesterday, 2026-11-01 or 2026-11-01T09:00)", s)
}

// modEntry is one selected component with its last-modification details.
type modEntry struct {
	Type       string    `json:"type"`
	Name       string    `json:"name"`
	ModifiedBy string    `json:"modifiedBy,omitempty"`
	Modified   time.Time `json:"modified,omitempty"`
	byID       string    // modifier's user Id when only the Id is known
}

type modSelection struct {
	Entries []modEntry
	Source  string         // "SourceMember" or "listMetadata"
	Hidden  map[string]int // type -> entries hidden as probable cascades
	Skipped []string       // types that could not be listed (all-types mode only)
}

// resolveModified selects the components in scope (the -m/-x package; empty
// means "any type") that match the filters. On a sandbox with source tracking
// it asks SourceMember — one query for every type — and otherwise falls back
// to listMetadata per type, which needs an explicit scope.
func resolveModified(ctx context.Context, org *auth.Org, client *mdapi.Client, catalog *mdapi.DescribeResult,
	scope *mdapi.Package, f *modFilter, apiVersion string, stage func(string)) (*modSelection, error) {

	now := time.Now()
	var since, until time.Time
	var err error
	if f.since != "" {
		if since, err = parseWhen(f.since, now); err != nil {
			return nil, fmt.Errorf("--since: %w", err)
		}
	}
	if f.until != "" {
		if until, err = parseWhen(f.until, now); err != nil {
			return nil, fmt.Errorf("--until: %w", err)
		}
	}
	if !since.IsZero() && !until.IsZero() && since.After(until) {
		return nil, fmt.Errorf("--since is later than --until")
	}

	sc := sfapi.New(org)
	sc.APIVersion = apiVersion

	var userIDs []string
	userByID := map[string]string{}
	if len(f.modifiedBy) > 0 {
		stage("resolving --modified-by users")
		users, err := sc.ResolveUsers(ctx, f.modifiedBy, org.Username)
		if err != nil {
			return nil, err
		}
		for _, u := range users {
			userIDs = append(userIDs, u.ID)
			userByID[id15(u.ID)] = u.Name
		}
	}
	inScope := scopeMatcher(scope)
	var excl func(typ, name string) bool
	var exclPkg *mdapi.Package
	if len(f.exclude) > 0 {
		var err error
		exclPkg, err = mdapi.ParseSpecifiers(f.exclude, apiVersion, mdapi.NewTypeResolver(catalog))
		if err != nil {
			return nil, fmt.Errorf("--exclude-metadata: %w", err)
		}
		excl = scopeMatcher(exclPkg)
	}

	// Sandboxes track every change; production orgs have no SourceMember.
	if org.IsSandbox && f.hasChangeFilter() && os.Getenv("SFF_NO_SOURCEMEMBER") == "" { // env: force the listMetadata path (testing)
		stage("querying SourceMember (sandbox change tracking)")
		if sms, err := sc.SourceMembers(ctx, since, until, userIDs); err == nil {
			entries, err := entriesFromSourceMembers(ctx, sc, sms, catalog, inScope, f.includePackages, userByID)
			if err != nil {
				return nil, err
			}
			if os.Getenv("SFF_NO_FASTLIST") == "" {
				// SourceMember misses components deployed via the Tooling API (and
				// other untracked paths), so top it up from the SOQL-listable types.
				extra := sc.FastListAll(ctx, sfapi.FastQuery{Since: since, Until: until, UserIDs: userIDs}, 6)
				entries = mergeFastEntries(entries, extra, inScope, f.includePackages, userByID)
				fillUserNames(ctx, sc, entries)
				sortEntries(entries)
			}
			return finalizeSelection(entries, "SourceMember", scope, excl, f), nil
		} else {
			stage("SourceMember unavailable (" + err.Error() + "); falling back to listMetadata")
		}
	}

	// Without source tracking the only way to find changes is to list components.
	// With -m/-x that is just the named types; without them, every type the org
	// describes (concurrently), skipping types excluded wholesale by -e.
	var types []string
	allTypes := scope == nil || len(scope.Types) == 0
	if allTypes {
		if catalog == nil {
			return nil, fmt.Errorf("select metadata with -m Type[:Name] (or -x): the org's type catalog is unavailable to list every type")
		}
		types = listableTypes(catalog, exclPkg)
		stage(fmt.Sprintf("no source tracking: listing %d metadata types via listMetadata (narrow with -m, skip types with -e)", len(types)))
	} else {
		for _, t := range scope.Types {
			types = append(types, t.Name)
		}
	}

	// Start the slow, large types first so they overlap with everything else.
	types = heavyFirst(types)
	// Types with a SOQL route are filtered server-side (one fast query);
	// everything else goes through listMetadata.
	fastQ := sfapi.FastQuery{Since: since, Until: until, UserIDs: userIDs}
	useFast := os.Getenv("SFF_NO_FASTLIST") == ""
	list := func(typ string) ([]mdapi.FileProperties, error) {
		if useFast {
			if es, handled, err := sc.FastListEntries(ctx, typ, fastQ); handled && err == nil {
				props := make([]mdapi.FileProperties, len(es))
				for i, e := range es {
					props[i] = mdapi.FileProperties{FullName: e.Name, LastModifiedByID: e.ChangedBy, LastModifiedDate: e.Modified.UTC().Format(time.RFC3339)}
					if e.Managed {
						props[i].ManageableState = "installed"
					}
				}
				return props, nil
			}
		}
		return client.ListTypeProps(ctx, typ)
	}
	results := listTypesConcurrently(list, types, 6, func(done int, running []string) {
		stage(fmt.Sprintf("listMetadata: %d/%d types done; in flight: %s", done, len(types), strings.Join(running, ", ")))
	})
	var entries []modEntry
	var skipped []string
	for i, typ := range types {
		res := results[i]
		if res.err != nil {
			if !allTypes {
				return nil, fmt.Errorf("list %s: %w", typ, res.err)
			}
			skipped = append(skipped, typ)
			continue
		}
		for _, p := range res.props {
			if !f.includePackages && p.ManageableState == "installed" {
				continue
			}
			if !inScope(typ, p.FullName) {
				continue
			}
			mod, _ := time.Parse(time.RFC3339, p.LastModifiedDate)
			if (!since.IsZero() || !until.IsZero()) && mod.IsZero() {
				continue
			}
			if !since.IsZero() && mod.Before(since) || !until.IsZero() && mod.After(until) {
				continue
			}
			if len(userIDs) > 0 && userByID[id15(p.LastModifiedByID)] == "" {
				continue
			}
			name := p.LastModifiedByName
			if name == "" {
				name = userByID[id15(p.LastModifiedByID)]
			}
			entries = append(entries, modEntry{Type: typ, Name: p.FullName, ModifiedBy: name, Modified: mod, byID: p.LastModifiedByID})
		}
	}
	fillUserNames(ctx, sc, entries)
	sortEntries(entries)
	sel := finalizeSelection(entries, "listMetadata", scope, excl, f)
	sel.Skipped = skipped
	return sel, nil
}

type listResult struct {
	props []mdapi.FileProperties
	err   error
}

// listTypesConcurrently lists every type with up to workers calls in flight and
// returns the results in types order. onDone is called (from worker goroutines)
// after each type finishes.
func listTypesConcurrently(list func(typ string) ([]mdapi.FileProperties, error), types []string, workers int, onDone func(done int, running []string)) []listResult {
	results := make([]listResult, len(types))
	jobs := make(chan int)
	var wg sync.WaitGroup
	var mu sync.Mutex
	done := 0
	running := map[string]bool{}
	for w := 0; w < workers; w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := range jobs {
				mu.Lock()
				running[types[i]] = true
				mu.Unlock()
				props, err := list(types[i])
				results[i] = listResult{props, err}
				mu.Lock()
				done++
				delete(running, types[i])
				n := done
				var now []string
				for t := range running {
					now = append(now, t)
				}
				mu.Unlock()
				sort.Strings(now)
				if onDone != nil {
					onDone(n, now)
				}
			}
		}()
	}
	for i := range types {
		jobs <- i
	}
	close(jobs)
	wg.Wait()
	return results
}

// heavyFirst moves the types that are slow to list in large orgs to the front,
// keeping the relative order of the rest.
func heavyFirst(types []string) []string {
	heavy := []string{"CustomField", "CustomObject", "ApexClass", "Layout", "RecordType", "ValidationRule",
		"PermissionSet", "Profile", "ListView", "LightningComponentBundle", "Flow", "FlexiPage", "CustomLabel", "Report", "Dashboard"}
	rank := map[string]int{}
	for i, t := range heavy {
		rank[t] = i
	}
	out := append([]string(nil), types...)
	sort.SliceStable(out, func(i, j int) bool {
		ri, iok := rank[out[i]]
		rj, jok := rank[out[j]]
		switch {
		case iok && jok:
			return ri < rj
		case iok:
			return true
		default:
			return false
		}
	})
	return out
}

// fillUserNames resolves display names for entries that only carry a user Id
// (rows found via SOQL), in one batched query.
func fillUserNames(ctx context.Context, sc *sfapi.Client, entries []modEntry) {
	var ids []string
	for _, e := range entries {
		if e.ModifiedBy == "" && e.byID != "" {
			ids = append(ids, e.byID)
		}
	}
	if len(ids) == 0 {
		return
	}
	names, err := sc.UserNames(ctx, ids)
	if err != nil {
		return
	}
	byID15 := map[string]string{}
	for id, n := range names {
		byID15[id15(id)] = n
	}
	for i := range entries {
		if entries[i].ModifiedBy == "" {
			entries[i].ModifiedBy = byID15[id15(entries[i].byID)]
		}
	}
}

// listableTypes returns every type name the catalog describes (parents and
// children), sorted, minus those the exclusion package drops entirely.
func listableTypes(catalog *mdapi.DescribeResult, excl *mdapi.Package) []string {
	skip := map[string]bool{}
	if excl != nil {
		for _, t := range excl.Types {
			for _, m := range t.Members {
				if m == "*" {
					skip[strings.ToLower(t.Name)] = true
				}
			}
		}
	}
	var out []string
	for name := range catalogTypes(catalog) {
		if !skip[strings.ToLower(name)] {
			out = append(out, name)
		}
	}
	sort.Strings(out)
	return out
}

// reportSkipped notes types that could not be listed in all-types mode.
func reportSkipped(w io.Writer, skipped []string) {
	if len(skipped) == 0 {
		return
	}
	shown := skipped
	more := ""
	if len(shown) > 8 {
		more = fmt.Sprintf(" and %d more", len(shown)-8)
		shown = shown[:8]
	}
	fmt.Fprintf(w, "skipped %d type(s) that could not be listed: %s%s\n", len(skipped), strings.Join(shown, ", "), more)
}

func id15(id string) string {
	if len(id) > 15 {
		return id[:15]
	}
	return id
}

// scopeMatcher returns a predicate telling whether (type, name) is selected by
// the -m/-x package; a nil or empty package selects everything.
func scopeMatcher(scope *mdapi.Package) func(typ, name string) bool {
	if scope == nil || len(scope.Types) == 0 {
		return func(string, string) bool { return true }
	}
	byType := map[string][]string{}
	for _, t := range scope.Types {
		byType[strings.ToLower(t.Name)] = append(byType[strings.ToLower(t.Name)], t.Members...)
	}
	return func(typ, name string) bool {
		members, ok := byType[strings.ToLower(typ)]
		if !ok {
			return false
		}
		for _, m := range members {
			if mdapi.MatchMember(m, name) {
				return true
			}
		}
		return false
	}
}

// entriesFromSourceMembers turns SourceMember rows into selectable components:
// child resources are folded into their bundle, types the Metadata API doesn't
// know are dropped, and installed-package components are excluded by default.
func entriesFromSourceMembers(ctx context.Context, sc *sfapi.Client, sms []sfapi.SourceMember, catalog *mdapi.DescribeResult,
	inScope func(typ, name string) bool, includePackages bool, userByID map[string]string) ([]modEntry, error) {

	known := catalogTypes(catalog)
	seen := map[string]bool{}
	var entries []modEntry
	var changedBy []string
	for _, sm := range sms { // newest first, so the first row per component wins
		typ, name, ok := normalizeSourceMember(sm.Type, sm.Name)
		if !ok || (known != nil && !known[typ]) {
			continue
		}
		if !includePackages && looksManaged(name) {
			continue
		}
		if !inScope(typ, name) || seen[typ+":"+name] {
			continue
		}
		seen[typ+":"+name] = true
		entries = append(entries, modEntry{Type: typ, Name: name, ModifiedBy: sm.ChangedBy, Modified: sm.Modified})
		changedBy = append(changedBy, sm.ChangedBy)
	}
	var missing []string
	for _, id := range changedBy {
		if userByID[id15(id)] == "" {
			missing = append(missing, id)
		}
	}
	if len(missing) > 0 {
		if names, err := sc.UserNames(ctx, missing); err == nil {
			for id, n := range names {
				userByID[id15(id)] = n
			}
		}
	}
	for i := range entries {
		if n := userByID[id15(entries[i].ModifiedBy)]; n != "" {
			entries[i].ModifiedBy = n
		}
	}
	sortEntries(entries)
	return entries, nil
}

// normalizeSourceMember maps a SourceMember row to a Metadata API component.
// Bundle resources ("bundle/file.js") fold into their bundle; ok is false for
// rows that are not retrievable on their own.
func normalizeSourceMember(typ, name string) (string, string, bool) {
	bundle := map[string]string{
		"LightningComponentResource": "LightningComponentBundle",
		"AuraDefinition":             "AuraDefinitionBundle",
		"ExperienceResource":         "ExperienceBundle",
	}
	if b, ok := bundle[typ]; ok {
		first, _, _ := strings.Cut(name, "/")
		return b, first, first != ""
	}
	return typ, name, name != ""
}

// catalogTypes returns every metadata type name (parents and children) the org
// describes, or nil when there is no catalog.
func catalogTypes(d *mdapi.DescribeResult) map[string]bool {
	if d == nil {
		return nil
	}
	m := map[string]bool{}
	for _, o := range d.Objects {
		m[o.Name] = true
		for _, c := range o.ChildXMLNames {
			m[c] = true
		}
	}
	return m
}

var managedRe = regexp.MustCompile(`^[A-Za-z][A-Za-z0-9]*__(.+)$`)

// looksManaged reports whether a component name carries a managed-package
// namespace ("ns__Thing"): namespaces hold no underscores, so "EUR_X__c" is
// not one but "clm__X__c" and "clm__X" are. Checked per "Object.Field" part.
func looksManaged(name string) bool {
	for _, part := range strings.Split(name, ".") {
		m := managedRe.FindStringSubmatch(part)
		if m == nil {
			continue
		}
		switch m[1] {
		case "c", "mdt", "e", "b", "x", "r", "kav", "xo", "share", "history", "feed", "tag", "changeevent":
			continue
		}
		return true
	}
	return false
}

func sortEntries(e []modEntry) {
	sort.Slice(e, func(i, j int) bool {
		if e[i].Type != e[j].Type {
			return e[i].Type < e[j].Type
		}
		return e[i].Name < e[j].Name
	})
}

// packageFromEntries builds the manifest for a selection.
func packageFromEntries(entries []modEntry, apiVersion string) *mdapi.Package {
	byType := map[string][]string{}
	var order []string
	for _, e := range entries {
		if _, ok := byType[e.Type]; !ok {
			order = append(order, e.Type)
		}
		byType[e.Type] = append(byType[e.Type], e.Name)
	}
	sort.Strings(order)
	pkg := &mdapi.Package{Version: strings.TrimPrefix(apiVersion, "v")}
	for _, t := range order {
		m := byType[t]
		sort.Strings(m)
		pkg.Types = append(pkg.Types, mdapi.PackageTypes{Name: t, Members: m})
	}
	return pkg
}

// printEntries renders a selection as an aligned table or JSON.
func printEntries(w io.Writer, entries []modEntry, asJSON bool) error {
	if asJSON {
		enc := json.NewEncoder(w)
		enc.SetIndent("", "  ")
		if entries == nil {
			entries = []modEntry{}
		}
		return enc.Encode(entries)
	}
	tw := tabwriter.NewWriter(w, 0, 4, 2, ' ', 0)
	fmt.Fprintln(tw, "TYPE\tNAME\tMODIFIED BY\tMODIFIED")
	for _, e := range entries {
		when := ""
		if !e.Modified.IsZero() {
			when = e.Modified.Local().Format("2006-01-02 15:04")
		}
		fmt.Fprintf(tw, "%s\t%s\t%s\t%s\n", e.Type, e.Name, e.ModifiedBy, when)
	}
	return tw.Flush()
}

// finalizeSelection applies --exclude-metadata and the cascade heuristic.
func finalizeSelection(entries []modEntry, source string, scope *mdapi.Package, excl func(typ, name string) bool, f *modFilter) *modSelection {
	sel := &modSelection{Source: source}
	if excl != nil {
		entries = applyExclusions(entries, excl, explicitMatcher(scope))
	}
	if !f.includeCascading {
		entries, sel.Hidden = hideCascades(entries, scopeTypeSet(scope))
	}
	sel.Entries = entries
	return sel
}

// explicitMatcher reports whether -m/-x names (type, name) beyond a bare type:
// a member that is a name or a pattern, not "*". Such components survive an
// --exclude-metadata of the same type.
func explicitMatcher(scope *mdapi.Package) func(typ, name string) bool {
	return func(typ, name string) bool {
		if scope == nil {
			return false
		}
		for _, t := range scope.Types {
			if !strings.EqualFold(t.Name, typ) {
				continue
			}
			for _, m := range t.Members {
				if m != "*" && mdapi.MatchMember(m, name) {
					return true
				}
			}
		}
		return false
	}
}

// applyExclusions drops entries matched by excl unless protected.
func applyExclusions(entries []modEntry, excl, protected func(typ, name string) bool) []modEntry {
	out := entries[:0:0]
	for _, e := range entries {
		if excl(e.Type, e.Name) && !protected(e.Type, e.Name) {
			continue
		}
		out = append(out, e)
	}
	return out
}

// cascadeTypes are the types whose timestamp is bumped as a side effect of
// unrelated changes (e.g. creating a field touches every permission set).
var cascadeTypes = map[string]bool{"PermissionSet": true, "PermissionSetGroup": true, "Profile": true}

const (
	cascadeMinEntries = 20
	cascadeWindow     = 5 * time.Minute
)

func scopeTypeSet(scope *mdapi.Package) map[string]bool {
	m := map[string]bool{}
	if scope != nil {
		for _, t := range scope.Types {
			m[strings.ToLower(t.Name)] = true
		}
	}
	return m
}

// hideCascades removes bursts — at least cascadeMinEntries entries of one
// cascade-prone type by one author inside cascadeWindow — that are most likely
// a side effect of another change. Types named explicitly by -m are never
// touched. It returns the kept entries and the number hidden per type.
func hideCascades(entries []modEntry, explicit map[string]bool) ([]modEntry, map[string]int) {
	type key struct{ typ, by string }
	groups := map[key][]int{}
	for i, e := range entries {
		if cascadeTypes[e.Type] && !explicit[strings.ToLower(e.Type)] && !e.Modified.IsZero() {
			k := key{e.Type, e.ModifiedBy}
			groups[k] = append(groups[k], i)
		}
	}
	drop := map[int]bool{}
	for _, idx := range groups {
		sort.Slice(idx, func(a, b int) bool { return entries[idx[a]].Modified.Before(entries[idx[b]].Modified) })
		lo := 0
		for hi := range idx {
			for entries[idx[hi]].Modified.Sub(entries[idx[lo]].Modified) > cascadeWindow {
				lo++
			}
			if hi-lo+1 >= cascadeMinEntries {
				for j := lo; j <= hi; j++ {
					drop[idx[j]] = true
				}
			}
		}
	}
	if len(drop) == 0 {
		return entries, nil
	}
	hidden := map[string]int{}
	kept := make([]modEntry, 0, len(entries)-len(drop))
	for i, e := range entries {
		if drop[i] {
			hidden[e.Type]++
			continue
		}
		kept = append(kept, e)
	}
	return kept, hidden
}

// reportHidden tells the user about entries hidden as probable cascades.
func reportHidden(w io.Writer, hidden map[string]int) {
	types := make([]string, 0, len(hidden))
	for t := range hidden {
		types = append(types, t)
	}
	sort.Strings(types)
	for _, t := range types {
		fmt.Fprintf(w, "hid %d %s entries modified in one burst by one user (likely a cascade from another change); --include-cascading or -m %s to show them\n", hidden[t], t, t)
	}
}

// mergeFastEntries adds SOQL-found components to entries, keeping the newer
// modification when a component appears in both.
func mergeFastEntries(entries []modEntry, extra map[string][]sfapi.FastEntry, inScope func(typ, name string) bool,
	includePackages bool, userByID map[string]string) []modEntry {

	idx := map[string]int{}
	for i, e := range entries {
		idx[e.Type+":"+e.Name] = i
	}
	for typ, es := range extra {
		for _, e := range es {
			if (e.Managed && !includePackages) || !inScope(typ, e.Name) {
				continue
			}
			ne := modEntry{Type: typ, Name: e.Name, ModifiedBy: userByID[id15(e.ChangedBy)], Modified: e.Modified, byID: e.ChangedBy}
			if i, ok := idx[typ+":"+e.Name]; ok {
				if e.Modified.After(entries[i].Modified) {
					entries[i] = ne
				}
				continue
			}
			idx[typ+":"+e.Name] = len(entries)
			entries = append(entries, ne)
		}
	}
	return entries
}
