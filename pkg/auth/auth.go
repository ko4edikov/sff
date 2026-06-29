// Package auth reads the Salesforce credentials that the official `sf`/`sfdx`
// CLI has already stored, without shelling out to `sf` (which is slow and, in
// newer versions, redacts secrets).
//
// Per-org auth lives in ~/.sfdx/<username>.json with accessToken/refreshToken
// encrypted via AES-256-GCM. The key is stored in the macOS Keychain
// (service=sfdx, account=local). The default org and aliases come from
// ~/.sf/config.json (target-org) / ~/.sfdx/sfdx-config.json (defaultusername)
// and ~/.sfdx/alias.json.
package auth

import "fmt"

// Org holds a single org's credentials with tokens already decrypted in memory.
type Org struct {
	Username     string
	Alias        string
	OrgID        string
	InstanceURL  string
	LoginURL     string
	ClientID     string
	AccessToken  string
	RefreshToken string
	IsSandbox    bool
}

// Resolve returns the org identified by target, which may be an alias, a
// username, or "" to use the configured default org. Tokens are decrypted.
func Resolve(target string) (*Org, error) {
	username, alias, err := resolveTarget(target)
	if err != nil {
		return nil, err
	}

	raw, err := loadAuthFile(username)
	if err != nil {
		return nil, err
	}

	key, err := keychainKey()
	if err != nil {
		return nil, err
	}

	access, err := decryptSecret(key, raw.AccessToken)
	if err != nil {
		return nil, fmt.Errorf("decrypt accessToken for %s: %w", username, err)
	}
	refresh, err := decryptSecret(key, raw.RefreshToken)
	if err != nil {
		return nil, fmt.Errorf("decrypt refreshToken for %s: %w", username, err)
	}

	return &Org{
		Username:     raw.Username,
		Alias:        alias,
		OrgID:        raw.OrgID,
		InstanceURL:  raw.InstanceURL,
		LoginURL:     raw.LoginURL,
		ClientID:     raw.ClientID,
		AccessToken:  access,
		RefreshToken: refresh,
		IsSandbox:    raw.IsSandbox,
	}, nil
}
