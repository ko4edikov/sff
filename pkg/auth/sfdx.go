package auth

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// authFile mirrors the subset of ~/.sfdx/<username>.json that sff needs.
// accessToken and refreshToken are stored encrypted (see crypto.go).
type authFile struct {
	Username     string `json:"username"`
	OrgID        string `json:"orgId"`
	InstanceURL  string `json:"instanceUrl"`
	LoginURL     string `json:"loginUrl"`
	ClientID     string `json:"clientId"`
	AccessToken  string `json:"accessToken"`
	RefreshToken string `json:"refreshToken"`
	IsSandbox    bool   `json:"isSandbox"`
}

func sfdxDir() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("locate home dir: %w", err)
	}
	return filepath.Join(home, ".sfdx"), nil
}

// loadAuthFile reads and parses ~/.sfdx/<username>.json.
func loadAuthFile(username string) (*authFile, error) {
	dir, err := sfdxDir()
	if err != nil {
		return nil, err
	}
	path := filepath.Join(dir, username+".json")
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, fmt.Errorf("no auth for %q (run `sf org login web` first)", username)
		}
		return nil, fmt.Errorf("read %s: %w", path, err)
	}
	var a authFile
	if err := json.Unmarshal(data, &a); err != nil {
		return nil, fmt.Errorf("parse %s: %w", path, err)
	}
	return &a, nil
}

// resolveTarget maps target (alias, username, or "" for default) to a concrete
// username, also returning the alias that pointed to it (if any).
func resolveTarget(target string) (username, alias string, err error) {
	aliases, err := loadAliases()
	if err != nil {
		return "", "", err
	}

	if target == "" {
		target, err = defaultTarget()
		if err != nil {
			return "", "", err
		}
		if target == "" {
			return "", "", fmt.Errorf("no target org given and no default org configured (use a target or set one with `sf config set target-org`)")
		}
	}

	if username, ok := aliases[target]; ok {
		return username, target, nil
	}
	// target is a username (or an alias that resolves indirectly via default).
	return target, aliasFor(aliases, target), nil
}

// loadAliases reads ~/.sfdx/alias.json -> {"orgs": {alias: username}}.
func loadAliases() (map[string]string, error) {
	dir, err := sfdxDir()
	if err != nil {
		return nil, err
	}
	data, err := os.ReadFile(filepath.Join(dir, "alias.json"))
	if err != nil {
		if os.IsNotExist(err) {
			return map[string]string{}, nil
		}
		return nil, fmt.Errorf("read alias.json: %w", err)
	}
	var parsed struct {
		Orgs map[string]string `json:"orgs"`
	}
	if err := json.Unmarshal(data, &parsed); err != nil {
		return nil, fmt.Errorf("parse alias.json: %w", err)
	}
	if parsed.Orgs == nil {
		parsed.Orgs = map[string]string{}
	}
	return parsed.Orgs, nil
}

// aliasFor returns the first alias pointing at username, or "".
func aliasFor(aliases map[string]string, username string) string {
	for a, u := range aliases {
		if u == username {
			return a
		}
	}
	return ""
}

// defaultTarget reads the configured default org, mirroring the Salesforce CLI's
// precedence: a project-local config (the nearest .sf/config.json or legacy
// .sfdx/sfdx-config.json found by walking up from the current directory) wins
// over the global config in the home directory. At each level target-org is
// preferred over the legacy defaultusername.
func defaultTarget() (string, error) {
	type source struct {
		dir      string
		file     string
		key      string
		explicit bool // dir was located (an empty dir means "not present")
	}
	localSf, okSf := findConfigDir(".sf")
	localSfdx, okSfdx := findConfigDir(".sfdx")
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("locate home dir: %w", err)
	}
	for _, s := range []source{
		{localSf, "config.json", "target-org", okSf},
		{localSfdx, "sfdx-config.json", "defaultusername", okSfdx},
		{filepath.Join(home, ".sf"), "config.json", "target-org", true},
		{filepath.Join(home, ".sfdx"), "sfdx-config.json", "defaultusername", true},
	} {
		if !s.explicit {
			continue
		}
		v, err := readConfigKey(s.dir, s.file, s.key)
		if err != nil {
			return "", err
		}
		if v != "" {
			return v, nil
		}
	}
	return "", nil
}

// findConfigDir walks up from the current directory looking for a child
// directory named name (".sf" or ".sfdx"), returning its path and whether one
// was found. This locates a project-local Salesforce config.
func findConfigDir(name string) (string, bool) {
	dir, err := os.Getwd()
	if err != nil {
		return "", false
	}
	for {
		cand := filepath.Join(dir, name)
		if fi, err := os.Stat(cand); err == nil && fi.IsDir() {
			return cand, true
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return "", false
		}
		dir = parent
	}
}

func readConfigKey(dir, file, key string) (string, error) {
	data, err := os.ReadFile(filepath.Join(dir, file))
	if err != nil {
		if os.IsNotExist(err) {
			return "", nil
		}
		return "", fmt.Errorf("read %s: %w", file, err)
	}
	var m map[string]json.RawMessage
	if err := json.Unmarshal(data, &m); err != nil {
		return "", fmt.Errorf("parse %s: %w", file, err)
	}
	raw, ok := m[key]
	if !ok {
		return "", nil
	}
	var s string
	if err := json.Unmarshal(raw, &s); err != nil {
		return "", fmt.Errorf("parse %s[%s]: %w", file, key, err)
	}
	return strings.TrimSpace(s), nil
}
