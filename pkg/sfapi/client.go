// Package sfapi is a thin Salesforce REST client built on the credentials read
// by internal/auth. It transparently refreshes an expired access token once on
// a 401 and retries the request.
package sfapi

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"
	"strings"

	"github.com/ko4edikov/sff/pkg/auth"
)

// DefaultAPIVersion is used when none is specified. Salesforce keeps old
// versions available, so a slightly conservative default is safe.
const DefaultAPIVersion = "v60.0"

// Client issues authenticated REST calls against a single org.
type Client struct {
	Org        *auth.Org
	APIVersion string
	HTTP       *http.Client
}

// New returns a Client for org using the default API version.
func New(org *auth.Org) *Client {
	return &Client{Org: org, APIVersion: DefaultAPIVersion, HTTP: http.DefaultClient}
}

// do executes an authenticated request against an instance-relative path
// (e.g. "/services/data/v60.0/query/?q=..."). On a 401 it refreshes the token
// once and retries. The caller owns closing the returned body.
func (c *Client) do(ctx context.Context, method, path string, body io.Reader) (*http.Response, error) {
	resp, err := c.send(ctx, method, path, body)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != http.StatusUnauthorized {
		return resp, nil
	}

	// Token likely expired: refresh once and retry.
	resp.Body.Close()
	if body != nil {
		// A retry needs a fresh body; callers that pass a non-nil body must use
		// doRetryable instead. Guard against silently re-sending an empty body.
		return nil, fmt.Errorf("got 401 and request body is not replayable")
	}
	if rerr := c.Org.Refresh(ctx); rerr != nil {
		return nil, fmt.Errorf("token refresh after 401: %w", rerr)
	}
	return c.send(ctx, method, path, nil)
}

// requestJSON sends method to path with an optional JSON body and returns the
// response status and body bytes. Unlike do, it can replay the request after a
// 401 refresh because it holds the body as bytes (do refuses to re-send a
// non-nil body). A nil body is sent without a Content-Type.
func (c *Client) requestJSON(ctx context.Context, method, path string, body []byte) (int, []byte, error) {
	send := func() (*http.Response, error) {
		var r io.Reader
		if body != nil {
			r = bytes.NewReader(body)
		}
		return c.send(ctx, method, path, r)
	}

	resp, err := send()
	if err != nil {
		return 0, nil, err
	}
	if resp.StatusCode == http.StatusUnauthorized {
		resp.Body.Close()
		if rerr := c.Org.Refresh(ctx); rerr != nil {
			return 0, nil, fmt.Errorf("token refresh after 401: %w", rerr)
		}
		if resp, err = send(); err != nil {
			return 0, nil, err
		}
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(resp.Body)
	if err != nil {
		return resp.StatusCode, nil, err
	}
	return resp.StatusCode, data, nil
}

func (c *Client) send(ctx context.Context, method, path string, body io.Reader) (*http.Response, error) {
	url := strings.TrimRight(c.Org.InstanceURL, "/") + path
	req, err := http.NewRequestWithContext(ctx, method, url, body)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+c.Org.AccessToken)
	req.Header.Set("Accept", "application/json")
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	return c.HTTP.Do(req)
}

// apiError extracts Salesforce's error array into a readable message.
func apiError(status int, body []byte) error {
	msg := strings.TrimSpace(string(body))
	if msg == "" {
		msg = http.StatusText(status)
	}
	return fmt.Errorf("salesforce API error (%d): %s", status, msg)
}
