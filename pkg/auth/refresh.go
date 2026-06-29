package auth

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
)

// Refresh exchanges the org's refresh token for a fresh access token using the
// OAuth refresh_token grant. The new token is updated on o in memory only; it
// is not written back to ~/.sfdx (sff is read-only with respect to sf's store
// for now), so a subsequent process will refresh again if needed.
func (o *Org) Refresh(ctx context.Context) error {
	if o.RefreshToken == "" {
		return fmt.Errorf("no refresh token available for %s", o.Username)
	}
	endpoint := strings.TrimRight(o.LoginURL, "/") + "/services/oauth2/token"
	form := url.Values{
		"grant_type":    {"refresh_token"},
		"refresh_token": {o.RefreshToken},
		"client_id":     {o.ClientID},
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, strings.NewReader(form.Encode()))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Accept", "application/json")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return fmt.Errorf("refresh request: %w", err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)

	var r struct {
		AccessToken      string `json:"access_token"`
		InstanceURL      string `json:"instance_url"`
		Error            string `json:"error"`
		ErrorDescription string `json:"error_description"`
	}
	if err := json.Unmarshal(body, &r); err != nil {
		return fmt.Errorf("parse refresh response (%d): %s", resp.StatusCode, string(body))
	}
	if r.Error != "" {
		return fmt.Errorf("refresh failed: %s: %s", r.Error, r.ErrorDescription)
	}
	if r.AccessToken == "" {
		return fmt.Errorf("refresh returned no access token (%d)", resp.StatusCode)
	}
	o.AccessToken = r.AccessToken
	if r.InstanceURL != "" {
		o.InstanceURL = r.InstanceURL
	}
	return nil
}
