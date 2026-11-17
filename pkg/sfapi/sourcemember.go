package sfapi

import (
	"context"
	"fmt"
	"regexp"
	"strings"
	"time"
)

// SourceMember is one row of the Tooling API's SourceMember table, which
// sandboxes and scratch orgs keep to track every metadata change.
type SourceMember struct {
	Type      string // MemberType, e.g. "ApexClass"
	Name      string // MemberName, e.g. "Account.EUR_Score__c"
	ChangedBy string // user Id of the last modifier
	Modified  time.Time
}

// SOQLTime formats t as a SOQL datetime literal.
func SOQLTime(t time.Time) string { return t.UTC().Format("2006-01-02T15:04:05Z") }

// SourceMembers returns the live (not deleted) components modified in
// [since, until] (zero times are unbounded) by any of userIDs (empty = anyone).
// It fails on orgs without source tracking (production).
func (c *Client) SourceMembers(ctx context.Context, since, until time.Time, userIDs []string) ([]SourceMember, error) {
	where := []string{"IsNameObsolete = false"}
	if !since.IsZero() {
		where = append(where, "LastModifiedDate >= "+SOQLTime(since))
	}
	if !until.IsZero() {
		where = append(where, "LastModifiedDate <= "+SOQLTime(until))
	}
	if len(userIDs) > 0 {
		// ChangedBy is stored as a 15-char Id; match both lengths to be safe.
		var ids []string
		for _, id := range userIDs {
			ids = append(ids, id)
			if len(id) > 15 {
				ids = append(ids, id[:15])
			}
		}
		where = append(where, "ChangedBy IN ("+quoteList(ids)+")")
	}
	soql := "SELECT MemberType, MemberName, ChangedBy, LastModifiedDate FROM SourceMember WHERE " +
		strings.Join(where, " AND ") + " ORDER BY LastModifiedDate DESC"
	recs, _, err := c.QueryTooling(ctx, soql)
	if err != nil {
		return nil, err
	}
	out := make([]SourceMember, 0, len(recs))
	for _, r := range recs {
		t, _ := time.Parse("2006-01-02T15:04:05.000-0700", Field(r, "LastModifiedDate"))
		out = append(out, SourceMember{
			Type: Field(r, "MemberType"), Name: Field(r, "MemberName"),
			ChangedBy: Field(r, "ChangedBy"), Modified: t,
		})
	}
	return out, nil
}

// User is the subset of a User row used to resolve and display modifiers.
type User struct{ ID, Name, Username string }

var userIDRe = regexp.MustCompile(`^005[A-Za-z0-9]{12}([A-Za-z0-9]{3})?$`)

// ResolveUsers maps user specifiers to Users. A spec is "me" (selfUsername), a
// user Id (005…), a username (contains "@"), or a display name that may carry
// * and ? wildcards. Every spec must match at least one user.
func (c *Client) ResolveUsers(ctx context.Context, specs []string, selfUsername string) ([]User, error) {
	var out []User
	seen := map[string]bool{}
	for _, spec := range specs {
		spec = strings.TrimSpace(spec)
		var cond string
		switch {
		case spec == "":
			continue
		case strings.EqualFold(spec, "me"):
			cond = "Username = '" + soqlEscape(selfUsername) + "'"
		case userIDRe.MatchString(spec):
			cond = "Id = '" + spec + "'"
		case strings.Contains(spec, "@"):
			cond = "Username = '" + soqlEscape(spec) + "'"
		default:
			like := strings.NewReplacer("*", "%", "?", "_").Replace(soqlEscape(spec))
			cond = "Name LIKE '" + like + "'"
		}
		recs, _, err := c.Query(ctx, "SELECT Id, Name, Username FROM User WHERE "+cond)
		if err != nil {
			return nil, fmt.Errorf("resolve user %q: %w", spec, err)
		}
		if len(recs) == 0 {
			return nil, fmt.Errorf("no user matches %q", spec)
		}
		for _, r := range recs {
			u := User{ID: Field(r, "Id"), Name: Field(r, "Name"), Username: Field(r, "Username")}
			if !seen[u.ID] {
				seen[u.ID] = true
				out = append(out, u)
			}
		}
	}
	return out, nil
}

// UserNames returns Id -> display name for the given user Ids.
func (c *Client) UserNames(ctx context.Context, ids []string) (map[string]string, error) {
	out := map[string]string{}
	for _, chunk := range chunkStrings(uniqueStrings(ids), 200) {
		recs, _, err := c.Query(ctx, "SELECT Id, Name FROM User WHERE Id IN ("+quoteList(chunk)+")")
		if err != nil {
			return nil, err
		}
		for _, r := range recs {
			out[Field(r, "Id")] = Field(r, "Name")
		}
	}
	return out, nil
}

func soqlEscape(s string) string {
	return strings.NewReplacer(`\`, `\\`, `'`, `\'`).Replace(s)
}
