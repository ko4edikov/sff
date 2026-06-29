package source

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

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
