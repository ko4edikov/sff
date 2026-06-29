package mdapi

import (
	"context"
	"encoding/base64"
	"encoding/xml"
	"fmt"
	"strings"
	"time"
)

// RetrieveResult is the outcome of a completed retrieve.
type RetrieveResult struct {
	ZipFile  []byte
	Status   string
	Success  bool
	Messages []string
}

// asyncResult mirrors the <result> of retrieve / checkRetrieveStatus.
type retrieveStartEnv struct {
	Result struct {
		ID    string `xml:"id"`
		Done  bool   `xml:"done"`
		State string `xml:"state"`
	} `xml:"Body>retrieveResponse>result"`
}

type retrieveStatusEnv struct {
	Result struct {
		Done     bool   `xml:"done"`
		Status   string `xml:"status"`
		Success  bool   `xml:"success"`
		ZipFile  string `xml:"zipFile"`
		Messages []struct {
			FileName string `xml:"fileName"`
			Problem  string `xml:"problem"`
		} `xml:"messages"`
	} `xml:"Body>checkRetrieveStatusResponse>result"`
}

// Retrieve starts an async retrieve and returns its job id.
func (c *Client) Retrieve(ctx context.Context, pkg *Package) (string, error) {
	unpackaged := unpackagedXML(pkg, c.APIVersion)
	body := `<met:retrieve><met:retrieveRequest>` +
		`<met:apiVersion>` + c.APIVersion + `</met:apiVersion>` +
		`<met:singlePackage>true</met:singlePackage>` +
		`<met:unpackaged>` + unpackaged + `</met:unpackaged>` +
		`</met:retrieveRequest></met:retrieve>`
	debugf("retrieve request unpackaged: %s", unpackaged)

	raw, err := c.call(ctx, "retrieve", body)
	if err != nil {
		return "", err
	}
	var env retrieveStartEnv
	if err := xml.Unmarshal(raw, &env); err != nil {
		return "", fmt.Errorf("parse retrieve response: %w", err)
	}
	debugf("retrieve response: id=%q done=%t state=%q", env.Result.ID, env.Result.Done, env.Result.State)
	if env.Result.ID == "" {
		return "", fmt.Errorf("retrieve returned no job id: %s", snippet(raw))
	}
	return env.Result.ID, nil
}

// CheckRetrieveStatus polls a retrieve job once, returning its current state.
func (c *Client) CheckRetrieveStatus(ctx context.Context, id string) (done bool, res *RetrieveResult, err error) {
	body := `<met:checkRetrieveStatus>` +
		`<met:asyncProcessId>` + xmlEscape(id) + `</met:asyncProcessId>` +
		`<met:includeZip>true</met:includeZip>` +
		`</met:checkRetrieveStatus>`

	raw, err := c.call(ctx, "checkRetrieveStatus", body)
	if err != nil {
		return false, nil, err
	}
	var env retrieveStatusEnv
	if err := xml.Unmarshal(raw, &env); err != nil {
		return false, nil, fmt.Errorf("parse checkRetrieveStatus response: %w", err)
	}
	r := env.Result
	debugf("checkRetrieveStatus: done=%t status=%q success=%t zipB64Len=%d messages=%d",
		r.Done, r.Status, r.Success, len(r.ZipFile), len(r.Messages))
	if !r.Done {
		return false, nil, nil
	}

	out := &RetrieveResult{Status: r.Status, Success: r.Success}
	for _, m := range r.Messages {
		out.Messages = append(out.Messages, strings.TrimSpace(m.FileName+": "+m.Problem))
	}
	if r.ZipFile != "" {
		zipBytes, derr := base64.StdEncoding.DecodeString(r.ZipFile)
		if derr != nil {
			return true, out, fmt.Errorf("decode retrieved zip: %w", derr)
		}
		out.ZipFile = zipBytes
	}
	return true, out, nil
}

// RetrieveAndWait starts a retrieve and polls until completion. progress, if
// non-nil, is called with each polled state for UI feedback.
func (c *Client) RetrieveAndWait(ctx context.Context, pkg *Package, progress func(attempt int)) (*RetrieveResult, error) {
	id, err := c.Retrieve(ctx, pkg)
	if err != nil {
		return nil, err
	}

	const (
		first = 1 * time.Second
		cap_  = 3 * time.Second
	)
	wait := first
	for attempt := 1; ; attempt++ {
		if progress != nil {
			progress(attempt)
		}
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(wait):
		}
		if wait < cap_ {
			wait += time.Second
		}

		done, res, err := c.CheckRetrieveStatus(ctx, id)
		if err != nil {
			return nil, err
		}
		if done {
			if !res.Success {
				detail := strings.Join(res.Messages, "; ")
				if detail == "" {
					detail = res.Status
				}
				return res, fmt.Errorf("retrieve %s: %s", res.Status, detail)
			}
			return res, nil
		}
	}
}

// unpackagedXML renders the package types as a SOAP body fragment (met: prefix),
// one <types> block per type with all its members, followed by <version>.
func unpackagedXML(pkg *Package, version string) string {
	var b strings.Builder
	for _, t := range pkg.Types {
		b.WriteString(`<met:types>`)
		for _, m := range t.Members {
			b.WriteString(`<met:members>`)
			b.WriteString(xmlEscape(m))
			b.WriteString(`</met:members>`)
		}
		b.WriteString(`<met:name>`)
		b.WriteString(xmlEscape(t.Name))
		b.WriteString(`</met:name></met:types>`)
	}
	b.WriteString(`<met:version>`)
	b.WriteString(version)
	b.WriteString(`</met:version>`)
	return b.String()
}
