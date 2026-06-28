// Package mdapi is a minimal SOAP client for the Salesforce Metadata API,
// built on the credentials from internal/auth. It hand-rolls SOAP envelopes
// with encoding/xml (no dependency) and refreshes the session once on an
// INVALID_SESSION_ID fault, mirroring the REST 401 retry in internal/sfapi.
package mdapi

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"strings"

	"github.com/ko4edikov/sff/internal/auth"
	"github.com/ko4edikov/sff/internal/sfapi"
)

// Client issues Metadata API SOAP calls against a single org.
type Client struct {
	Org        *auth.Org
	APIVersion string // numeric, e.g. "60.0"
	HTTP       *http.Client
}

// New returns a Client for org using the default API version.
func New(org *auth.Org) *Client {
	return &Client{
		Org:        org,
		APIVersion: numericVersion(sfapi.DefaultAPIVersion),
		HTTP:       http.DefaultClient,
	}
}

func (c *Client) endpoint() string {
	return strings.TrimRight(c.Org.InstanceURL, "/") + "/services/Soap/m/" + c.APIVersion
}

// call wraps innerBody in a SOAP envelope, POSTs it, and returns the raw
// response body. On an INVALID_SESSION_ID fault it refreshes the token once and
// retries. action is the SOAPAction header value (Salesforce accepts "").
func (c *Client) call(ctx context.Context, action, innerBody string) ([]byte, error) {
	body, status, err := c.send(ctx, action, innerBody)
	if err != nil {
		return nil, err
	}
	if fault := parseFault(body); fault != nil {
		if fault.isInvalidSession() {
			if rerr := c.Org.Refresh(ctx); rerr != nil {
				return nil, fmt.Errorf("token refresh after INVALID_SESSION_ID: %w", rerr)
			}
			body, status, err = c.send(ctx, action, innerBody)
			if err != nil {
				return nil, err
			}
			if fault := parseFault(body); fault != nil {
				return nil, fault
			}
			return body, nil
		}
		return nil, fault
	}
	if status >= 300 {
		return nil, fmt.Errorf("metadata API HTTP %d: %s", status, snippet(body))
	}
	return body, nil
}

func (c *Client) send(ctx context.Context, action, innerBody string) ([]byte, int, error) {
	envelope := `<?xml version="1.0" encoding="UTF-8"?>` +
		`<soapenv:Envelope xmlns:soapenv="http://schemas.xmlsoap.org/soap/envelope/" xmlns:met="` + metadataNS + `">` +
		`<soapenv:Header><met:SessionHeader><met:sessionId>` + xmlEscape(c.Org.AccessToken) + `</met:sessionId></met:SessionHeader></soapenv:Header>` +
		`<soapenv:Body>` + innerBody + `</soapenv:Body>` +
		`</soapenv:Envelope>`

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.endpoint(), strings.NewReader(envelope))
	if err != nil {
		return nil, 0, err
	}
	req.Header.Set("Content-Type", "text/xml; charset=UTF-8")
	req.Header.Set("SOAPAction", `"`+action+`"`)

	resp, err := c.HTTP.Do(req)
	if err != nil {
		return nil, 0, fmt.Errorf("metadata API request: %w", err)
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, resp.StatusCode, fmt.Errorf("read metadata API response: %w", err)
	}
	return data, resp.StatusCode, nil
}

func snippet(b []byte) string {
	s := strings.TrimSpace(string(b))
	if len(s) > 500 {
		s = s[:500] + "…"
	}
	return s
}

func xmlEscape(s string) string {
	r := strings.NewReplacer("&", "&amp;", "<", "&lt;", ">", "&gt;", `"`, "&quot;")
	return r.Replace(s)
}
