package source

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"

	"github.com/ko4edikov/sff/pkg/mdapi"
	"github.com/ko4edikov/sff/pkg/project"
	"github.com/ko4edikov/sff/pkg/sfapi"
)

// RemoteFile is one file's source as it exists in the org. Rel is empty for a
// flat component and the bundle-relative path for LWC/Aura.
type RemoteFile struct {
	Rel     string
	Content []byte
}

// Fetch retrieves the org-side source for a target via the Tooling API.
func Fetch(ctx context.Context, c *sfapi.Client, t *Target) ([]RemoteFile, error) {
	switch t.Kind {
	case Flat:
		return fetchFlat(ctx, c, t)
	case LWC:
		return fetchLWC(ctx, c, t)
	case Aura:
		return fetchAura(ctx, c, t)
	default:
		return nil, fmt.Errorf("unsupported metadata kind")
	}
}

// RetrievedFile pairs the org-side source for one component file (materialized
// under a temp dir) with its local counterpart, both as ready-to-diff paths.
type RetrievedFile struct {
	Rel        string // source-relative path (e.g. objects/Account/fields/X__c.field-meta.xml)
	RemotePath string // org version, written under a temp dir by the converter
	LocalPath  string // local counterpart (may not exist on disk)
}

// FetchRetrieve pulls a Retrieve target from the org via the Metadata API,
// converts the result back to source format (decomposing it the same way the
// local project is laid out), and returns the per-file diff pairs. The returned
// cleanup function removes the temp dir holding the org-side source and must be
// called once the caller has finished diffing. files is nil when the org returns
// nothing for the requested component.
func FetchRetrieve(ctx context.Context, c *mdapi.Client, catalog *mdapi.DescribeResult, t *Target) (files []RetrievedFile, cleanup func(), err error) {
	pkg, err := mdapi.ParseSpecifiers([]string{t.RetrieveType + ":" + t.RetrieveMember}, c.APIVersion)
	if err != nil {
		return nil, nil, err
	}
	res, err := c.RetrieveAndWait(ctx, pkg, nil)
	if err != nil {
		return nil, nil, err
	}

	tmpRoot, err := os.MkdirTemp("", "sff-diff-org-")
	if err != nil {
		return nil, nil, err
	}
	cleanup = func() { _ = os.RemoveAll(tmpRoot) }
	tmpProj := &project.Project{Root: tmpRoot, Dirs: []project.Dir{{Path: "force-app", Default: true}}}

	conv, err := ConvertZipToSource(res.ZipFile, tmpProj, catalog)
	if err != nil {
		cleanup()
		return nil, nil, err
	}
	if len(conv.Written) == 0 {
		cleanup()
		detail := strings.Join(res.Messages, "; ")
		if detail == "" {
			detail = "not found in org"
		}
		return nil, nil, fmt.Errorf("%s: %s", t.Name, detail)
	}

	written := filterScope(conv.Written, t.ScopeRel)
	for _, rel := range written {
		files = append(files, RetrievedFile{
			Rel:        rel,
			RemotePath: placeInProject(tmpProj, rel),
			LocalPath:  placeInProject(t.Project, rel),
		})
	}
	return files, cleanup, nil
}

// filterScope narrows written to just scope when it is set and present; if scope
// is empty or matches nothing (e.g. a static resource whose content file name
// shifts during conversion), the full set is returned.
func filterScope(written []string, scope string) []string {
	if scope == "" {
		return written
	}
	for _, rel := range written {
		if rel == scope {
			return []string{rel}
		}
	}
	return written
}

func fetchFlat(ctx context.Context, c *sfapi.Client, t *Target) ([]RemoteFile, error) {
	soql := fmt.Sprintf("SELECT %s FROM %s WHERE Name = '%s' AND NamespacePrefix = null",
		t.Field, t.Object, soqlEscape(t.Name))
	records, _, err := c.QueryTooling(ctx, soql)
	if err != nil {
		return nil, err
	}
	if len(records) == 0 {
		return nil, fmt.Errorf("not found in org: %s", t.Name)
	}
	return []RemoteFile{{Rel: "", Content: []byte(stringField(records[0], t.Field))}}, nil
}

func fetchLWC(ctx context.Context, c *sfapi.Client, t *Target) ([]RemoteFile, error) {
	soql := fmt.Sprintf(`SELECT FilePath, Source FROM LightningComponentResource `+
		`WHERE LightningComponentBundle.DeveloperName = '%s' `+
		`AND LightningComponentBundle.NamespacePrefix = null`, soqlEscape(t.Name))
	records, _, err := c.QueryTooling(ctx, soql)
	if err != nil {
		return nil, err
	}
	prefix := "lwc/" + t.Name + "/"
	var files []RemoteFile
	for _, r := range records {
		rel := strings.TrimPrefix(stringField(r, "FilePath"), prefix)
		files = append(files, RemoteFile{Rel: rel, Content: []byte(stringField(r, "Source"))})
	}
	if len(files) == 0 {
		return nil, fmt.Errorf("bundle not found in org: %s", t.Name)
	}
	return files, nil
}

func fetchAura(ctx context.Context, c *sfapi.Client, t *Target) ([]RemoteFile, error) {
	soql := fmt.Sprintf(`SELECT DefType, Source FROM AuraDefinition `+
		`WHERE AuraDefinitionBundle.DeveloperName = '%s' `+
		`AND AuraDefinitionBundle.NamespacePrefix = null`, soqlEscape(t.Name))
	records, _, err := c.QueryTooling(ctx, soql)
	if err != nil {
		return nil, err
	}
	var files []RemoteFile
	for _, r := range records {
		name := auraFilename(t.Name, stringField(r, "DefType"))
		if name == "" {
			continue // unknown DefType
		}
		files = append(files, RemoteFile{Rel: name, Content: []byte(stringField(r, "Source"))})
	}
	if len(files) == 0 {
		return nil, fmt.Errorf("bundle not found in org: %s", t.Name)
	}
	return files, nil
}

// auraFilename maps an Aura DefType to the file it lives in (mirrors the
// sf-compare aura_filename map).
func auraFilename(name, defType string) string {
	switch defType {
	case "COMPONENT":
		return name + ".cmp"
	case "APPLICATION":
		return name + ".app"
	case "EVENT":
		return name + ".evt"
	case "INTERFACE":
		return name + ".intf"
	case "CONTROLLER":
		return name + "Controller.js"
	case "HELPER":
		return name + "Helper.js"
	case "RENDERER":
		return name + "Renderer.js"
	case "STYLE":
		return name + ".css"
	case "DESIGN":
		return name + ".design"
	case "SVG":
		return name + ".svg"
	case "DOCUMENTATION":
		return name + ".auradoc"
	case "TOKENS":
		return name + ".tokens"
	default:
		return ""
	}
}

// stringField extracts a record's field as a string (empty if absent/null).
func stringField(record json.RawMessage, field string) string {
	var m map[string]json.RawMessage
	if err := json.Unmarshal(record, &m); err != nil {
		return ""
	}
	raw, ok := m[field]
	if !ok {
		return ""
	}
	var s string
	if err := json.Unmarshal(raw, &s); err != nil {
		return ""
	}
	return s
}

// soqlEscape escapes a value for use inside a single-quoted SOQL string literal.
func soqlEscape(s string) string {
	return strings.NewReplacer(`\`, `\\`, `'`, `\'`).Replace(s)
}
