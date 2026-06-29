package auth

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// OrgSummary describes one authenticated org for `sff org list`. Tokens are not
// decrypted — listing only needs the public metadata.
type OrgSummary struct {
	Username    string   `json:"username"`
	Aliases     []string `json:"aliases,omitempty"`
	OrgID       string   `json:"orgId"`
	InstanceURL string   `json:"instanceUrl"`
	IsSandbox   bool     `json:"isSandbox"`
	IsScratch   bool     `json:"isScratch"`
	IsDevHub    bool     `json:"isDevHub"`
	IsDefault   bool     `json:"isDefault"`
}

// nonOrgFiles are files in ~/.sfdx that are not per-org auth files.
var nonOrgFiles = map[string]bool{
	"alias.json":       true,
	"sfdx-config.json": true,
	"key.json":         true,
	"stash.json":       true,
}

// ListOrgs enumerates the authenticated orgs stored by sf in ~/.sfdx.
func ListOrgs() ([]*OrgSummary, error) {
	dir, err := sfdxDir()
	if err != nil {
		return nil, err
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", dir, err)
	}

	aliasMap, err := loadAliases() // alias -> username
	if err != nil {
		return nil, err
	}
	revAlias := map[string][]string{}
	for a, u := range aliasMap {
		revAlias[u] = append(revAlias[u], a)
	}
	for _, as := range revAlias {
		sort.Strings(as)
	}

	def, _ := defaultTarget()
	defUser := def
	if u, ok := aliasMap[def]; ok {
		defUser = u
	}

	var orgs []*OrgSummary
	for _, e := range entries {
		name := e.Name()
		if e.IsDir() || !strings.HasSuffix(name, ".json") || nonOrgFiles[name] {
			continue
		}
		data, err := os.ReadFile(filepath.Join(dir, name))
		if err != nil {
			continue
		}
		var a struct {
			Username    string `json:"username"`
			OrgID       string `json:"orgId"`
			InstanceURL string `json:"instanceUrl"`
			IsSandbox   bool   `json:"isSandbox"`
			IsScratch   bool   `json:"isScratch"`
			IsDevHub    bool   `json:"isDevHub"`
		}
		// Skip non-auth stubs (e.g. *.sandbox.json sandbox-tracking files) that
		// carry a username but no real org identity.
		if json.Unmarshal(data, &a) != nil || a.Username == "" || a.OrgID == "" || a.InstanceURL == "" {
			continue
		}
		orgs = append(orgs, &OrgSummary{
			Username:    a.Username,
			Aliases:     revAlias[a.Username],
			OrgID:       a.OrgID,
			InstanceURL: a.InstanceURL,
			IsSandbox:   a.IsSandbox,
			IsScratch:   a.IsScratch,
			IsDevHub:    a.IsDevHub,
			IsDefault:   a.Username == defUser,
		})
	}

	sort.Slice(orgs, func(i, j int) bool {
		if orgs[i].IsDefault != orgs[j].IsDefault {
			return orgs[i].IsDefault // default first
		}
		return sortKey(orgs[i]) < sortKey(orgs[j])
	})
	return orgs, nil
}

// sortKey orders orgs by their first alias, falling back to username.
func sortKey(o *OrgSummary) string {
	if len(o.Aliases) > 0 {
		return o.Aliases[0]
	}
	return o.Username
}
