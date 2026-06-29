package mdapi

import (
	"context"
	"encoding/json"
	"encoding/xml"
	"fmt"
	"os"
	"path/filepath"
	"sort"
)

// MetadataObject is one entry of describeMetadata's result: a metadata type and
// its source-layout hints. It mirrors the columns of `sf org list metadata-types`.
type MetadataObject struct {
	Name          string   `xml:"xmlName" json:"xmlName"`
	ChildXMLNames []string `xml:"childXmlNames" json:"childXmlNames,omitempty"`
	DirectoryName string   `xml:"directoryName" json:"directoryName"`
	InFolder      bool     `xml:"inFolder" json:"inFolder"`
	MetaFile      bool     `xml:"metaFile" json:"metaFile"`
	Suffix        string   `xml:"suffix" json:"suffix,omitempty"`
}

// DescribeResult is the parsed describeMetadata response.
type DescribeResult struct {
	Objects            []MetadataObject `json:"metadataObjects"`
	OrganizationNS     string           `json:"organizationNamespace,omitempty"`
	PartialSaveAllowed bool             `json:"partialSaveAllowed"`
	TestRequired       bool             `json:"testRequired"`
}

type describeEnv struct {
	Result struct {
		MetadataObjects    []MetadataObject `xml:"metadataObjects"`
		OrganizationNS     string           `xml:"organizationNamespace"`
		PartialSaveAllowed bool             `xml:"partialSaveAllowed"`
		TestRequired       bool             `xml:"testRequired"`
	} `xml:"Body>describeMetadataResponse>result"`
}

// DescribeMetadata calls describeMetadata for the client's API version and
// returns the org's metadata type catalog, sorted by XML name.
func (c *Client) DescribeMetadata(ctx context.Context) (*DescribeResult, error) {
	body := `<met:describeMetadata><met:asOfVersion>` + c.APIVersion + `</met:asOfVersion></met:describeMetadata>`
	raw, err := c.call(ctx, "describeMetadata", body)
	if err != nil {
		return nil, err
	}
	var env describeEnv
	if err := xml.Unmarshal(raw, &env); err != nil {
		return nil, fmt.Errorf("parse describeMetadata response: %w", err)
	}
	objs := env.Result.MetadataObjects
	if len(objs) == 0 {
		return nil, fmt.Errorf("describeMetadata returned no types: %s", snippet(raw))
	}
	sort.Slice(objs, func(i, j int) bool { return objs[i].Name < objs[j].Name })
	return &DescribeResult{
		Objects:            objs,
		OrganizationNS:     env.Result.OrganizationNS,
		PartialSaveAllowed: env.Result.PartialSaveAllowed,
		TestRequired:       env.Result.TestRequired,
	}, nil
}

// DescribeMetadataCached returns the type catalog from an on-disk cache keyed by
// org id and API version, falling back to a live describeMetadata call (and
// populating the cache) on a miss. forceRefresh always calls the org. The bool
// reports whether the result came from cache. The catalog changes rarely, so
// the cache has no expiry; use forceRefresh to update it.
func (c *Client) DescribeMetadataCached(ctx context.Context, forceRefresh bool) (*DescribeResult, bool, error) {
	path := c.describeCachePath()
	if !forceRefresh && path != "" {
		if res, ok := readDescribeCache(path); ok {
			return res, true, nil
		}
	}
	res, err := c.DescribeMetadata(ctx)
	if err != nil {
		return nil, false, err
	}
	if path != "" {
		writeDescribeCache(path, res) // best-effort; a cache write failure isn't fatal
	}
	return res, false, nil
}

// describeCachePath is ~/.sff/describe-<orgid>-v<apiVersion>.json, or "" if no
// home dir or org id is available.
func (c *Client) describeCachePath() string {
	home, err := os.UserHomeDir()
	if err != nil || c.Org == nil || c.Org.OrgID == "" {
		return ""
	}
	name := fmt.Sprintf("describe-%s-v%s.json", c.Org.OrgID, c.APIVersion)
	return filepath.Join(home, ".sff", name)
}

func readDescribeCache(path string) (*DescribeResult, bool) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, false
	}
	var res DescribeResult
	if err := json.Unmarshal(data, &res); err != nil || len(res.Objects) == 0 {
		return nil, false
	}
	return &res, true
}

func writeDescribeCache(path string, res *DescribeResult) {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return
	}
	data, err := json.MarshalIndent(res, "", "  ")
	if err != nil {
		return
	}
	_ = os.WriteFile(path, data, 0o644)
}
