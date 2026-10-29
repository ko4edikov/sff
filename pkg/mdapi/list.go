package mdapi

import (
	"context"
	"encoding/xml"
	"fmt"
	"regexp"
	"sort"
	"strings"
)

// maxListQueries is the Metadata API cap on queries per listMetadata call.
const maxListQueries = 3

// folderTypes maps a folder-based metadata type to the type that lists its
// folders. The members of such a type live in a folder ("Folder/Name"), so
// listing them takes one query per folder.
var folderTypes = map[string]string{
	"Dashboard":     "DashboardFolder",
	"Document":      "DocumentFolder",
	"EmailTemplate": "EmailFolder",
	"Report":        "ReportFolder",
}

// ListQuery is one listMetadata query: a type and, for folder-based types, the
// folder to list.
type ListQuery struct {
	Type   string
	Folder string
}

// FileProperties is the subset of listMetadata's FileProperties we use.
type FileProperties struct {
	FullName string `xml:"fullName"`
	Type     string `xml:"type"`
}

type listEnv struct {
	Results []FileProperties `xml:"Body>listMetadataResponse>result"`
}

// ListMetadata calls listMetadata for the queries, splitting them into batches
// of at most three (the API limit), and returns every component found.
func (c *Client) ListMetadata(ctx context.Context, queries []ListQuery) ([]FileProperties, error) {
	var out []FileProperties
	for start := 0; start < len(queries); start += maxListQueries {
		end := min(start+maxListQueries, len(queries))
		var b strings.Builder
		b.WriteString(`<met:listMetadata>`)
		for _, q := range queries[start:end] {
			b.WriteString(`<met:queries><met:type>` + xmlEscape(q.Type) + `</met:type>`)
			if q.Folder != "" {
				b.WriteString(`<met:folder>` + xmlEscape(q.Folder) + `</met:folder>`)
			}
			b.WriteString(`</met:queries>`)
		}
		b.WriteString(`<met:asOfVersion>` + c.APIVersion + `</met:asOfVersion></met:listMetadata>`)

		raw, err := c.call(ctx, "listMetadata", b.String())
		if err != nil {
			return nil, err
		}
		var env listEnv
		if err := xml.Unmarshal(raw, &env); err != nil {
			return nil, fmt.Errorf("parse listMetadata response: %w", err)
		}
		out = append(out, env.Results...)
	}
	return out, nil
}

// listNames returns the fullName of every component of typ in the org. For a
// folder-based type it lists the folders first, then the contents of each, and
// includes the folders themselves.
func (c *Client) listNames(ctx context.Context, typ string) ([]string, error) {
	folderType, inFolder := folderTypes[typ]
	if !inFolder {
		props, err := c.ListMetadata(ctx, []ListQuery{{Type: typ}})
		if err != nil {
			return nil, err
		}
		return fullNames(props), nil
	}

	folders, err := c.ListMetadata(ctx, []ListQuery{{Type: folderType}})
	if err != nil {
		return nil, err
	}
	names := fullNames(folders)
	queries := make([]ListQuery, len(names))
	for i, f := range names {
		queries[i] = ListQuery{Type: typ, Folder: f}
	}
	items, err := c.ListMetadata(ctx, queries)
	if err != nil {
		return nil, err
	}
	return append(names, fullNames(items)...), nil
}

func fullNames(props []FileProperties) []string {
	names := make([]string, len(props))
	for i, p := range props {
		names[i] = p.FullName
	}
	return names
}

// IsGlob reports whether a member name is a partial pattern ("Foo*", "A?b")
// that the org can't resolve itself. A bare "*" is not one: the Metadata API
// expands it server-side.
func IsGlob(member string) bool {
	return member != "*" && strings.ContainsAny(member, "*?")
}

// globRegexp compiles a member pattern ("*" any run, "?" one character) into a
// case-insensitive, fully anchored regexp. Metadata names are case-insensitive.
func globRegexp(pattern string) *regexp.Regexp {
	var b strings.Builder
	b.WriteString("(?i)^")
	for _, r := range pattern {
		switch r {
		case '*':
			b.WriteString(".*")
		case '?':
			b.WriteString(".")
		default:
			b.WriteString(regexp.QuoteMeta(string(r)))
		}
	}
	b.WriteString("$")
	return regexp.MustCompile(b.String())
}

// ExpandGlobs replaces every partial-wildcard member in pkg ("Foo*") with the
// org's matching components, found via listMetadata. Plain names and a bare "*"
// are left untouched, and a type is only listed when it has a pattern. A
// pattern matching nothing is an error rather than a silent empty retrieve.
func (c *Client) ExpandGlobs(ctx context.Context, pkg *Package) error {
	for i := range pkg.Types {
		t := &pkg.Types[i]
		hasGlob := false
		for _, m := range t.Members {
			if IsGlob(m) {
				hasGlob = true
				break
			}
		}
		if !hasGlob {
			continue
		}

		names, err := c.listNames(ctx, t.Name)
		if err != nil {
			return fmt.Errorf("list %s to expand pattern: %w", t.Name, err)
		}

		seen := map[string]bool{}
		var expanded []string
		add := func(m string) {
			if !seen[m] {
				seen[m] = true
				expanded = append(expanded, m)
			}
		}
		for _, m := range t.Members {
			if !IsGlob(m) {
				add(m)
				continue
			}
			re := globRegexp(m)
			matched := 0
			for _, n := range names {
				if re.MatchString(n) {
					add(n)
					matched++
				}
			}
			if matched == 0 {
				return fmt.Errorf("no %s matches %q in the org", t.Name, m)
			}
		}
		sort.Strings(expanded)
		t.Members = expanded
	}
	return nil
}
