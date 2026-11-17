package sfapi

import (
	"context"
	"encoding/json"
	"strings"
)

// listSource describes how to enumerate one metadata type with a single SOQL
// query instead of the (much slower) Metadata API listMetadata call.
type listSource struct {
	tooling bool   // query the Tooling API (else the standard REST API)
	object  string // sObject to query
	nameCol string // column holding the component's developer name
	nsCol   string // namespace-prefix column ("" = none)
	parent  string // parent column for "Parent.Name" members ("" = none)
}

// listSources are the metadata types whose members can be enumerated by SOQL.
// Everything else falls back to listMetadata.
var listSources = map[string]listSource{
	"ApexClass":                {tooling: true, object: "ApexClass", nameCol: "Name", nsCol: "NamespacePrefix"},
	"ApexTrigger":              {tooling: true, object: "ApexTrigger", nameCol: "Name", nsCol: "NamespacePrefix"},
	"ApexPage":                 {tooling: true, object: "ApexPage", nameCol: "Name", nsCol: "NamespacePrefix"},
	"ApexComponent":            {tooling: true, object: "ApexComponent", nameCol: "Name", nsCol: "NamespacePrefix"},
	"StaticResource":           {tooling: true, object: "StaticResource", nameCol: "Name", nsCol: "NamespacePrefix"},
	"AuraDefinitionBundle":     {tooling: true, object: "AuraDefinitionBundle", nameCol: "DeveloperName", nsCol: "NamespacePrefix"},
	"LightningComponentBundle": {tooling: true, object: "LightningComponentBundle", nameCol: "DeveloperName", nsCol: "NamespacePrefix"},
	"RecordType":               {object: "RecordType", nameCol: "DeveloperName", nsCol: "NamespacePrefix", parent: "SobjectType"},
	"ValidationRule":           {tooling: true, object: "ValidationRule", nameCol: "ValidationName", nsCol: "NamespacePrefix", parent: "EntityDefinition.QualifiedApiName"},
}

// FastListNames returns the fullName of every component of typ using SOQL. The
// bool is false when typ has no fast path (the caller should use listMetadata);
// an error also means "fall back" to a careful caller. patterns are the glob
// members being expanded and are used only to narrow the query where safe.
func (c *Client) FastListNames(ctx context.Context, typ string, patterns []string) ([]string, bool, error) {
	if typ == "CustomField" {
		names, err := c.listCustomFields(ctx, patterns)
		return names, true, err
	}
	if typ == "CustomObject" {
		// SOQL can't enumerate every standard object listMetadata returns, so
		// only take the fast path when no pattern can match one: standard
		// object names contain no underscore, so a literal "_" before the first
		// wildcard rules them out.
		for _, p := range patterns {
			if !strings.Contains(p[:strings.IndexAny(p+"*", "*?")], "_") {
				return nil, false, nil
			}
		}
		names, err := c.listCustomObjects(ctx)
		return names, true, err
	}
	src, ok := listSources[typ]
	if !ok {
		return nil, false, nil
	}
	cols := []string{src.nameCol}
	if src.nsCol != "" {
		cols = append(cols, src.nsCol)
	}
	if src.parent != "" {
		cols = append(cols, src.parent)
	}
	soql := "SELECT " + strings.Join(cols, ", ") + " FROM " + src.object
	var recs []json.RawMessage
	var err error
	if src.tooling {
		recs, _, err = c.QueryTooling(ctx, soql)
	} else {
		recs, _, err = c.Query(ctx, soql)
	}
	if err != nil {
		return nil, true, err
	}
	names := make([]string, 0, len(recs))
	for _, r := range recs {
		name := Field(r, src.nameCol)
		if name == "" {
			continue
		}
		if src.nsCol != "" {
			name = withNamespace(Field(r, src.nsCol), name)
		}
		if src.parent != "" {
			name = fieldPath(r, src.parent) + "." + name
		}
		names = append(names, name)
	}
	return names, true, nil
}

// withNamespace prefixes a managed-package name with "ns__".
func fieldPath(rec json.RawMessage, path string) string {
	cur := rec
	for _, part := range strings.Split(path, ".") {
		var m map[string]json.RawMessage
		if err := json.Unmarshal(cur, &m); err != nil {
			return ""
		}
		cur = m[part]
	}
	return Field(json.RawMessage(`{"v":`+string(cur)+`}`), "v")
}

func withNamespace(ns, name string) string {
	if ns == "" {
		return name
	}
	return ns + "__" + name
}

// listCustomFields enumerates "Object.Field__c" members. Tooling's CustomField
// keys the owner by TableEnumOrId (an object API name for standard objects, a
// 01I… Id for custom ones), so custom-object Ids are resolved in a second query.
// The field-name part of each pattern narrows the query server-side.
func (c *Client) listCustomFields(ctx context.Context, patterns []string) ([]string, error) {
	soql := "SELECT TableEnumOrId, DeveloperName, NamespacePrefix FROM CustomField"
	if where := customFieldWhere(patterns); where != "" {
		soql += " WHERE " + where
	}
	fields, _, err := c.QueryTooling(ctx, soql)
	if err != nil {
		return nil, err
	}
	// Custom objects' EntityDefinition.DurableId is their 01I… Id and
	// QualifiedApiName carries the right suffix (__c, __mdt, __e, ...). Key by
	// the 15-char prefix since TableEnumOrId may be a 15- or 18-char Id.
	objs, _, err := c.Query(ctx, "SELECT DurableId, QualifiedApiName FROM EntityDefinition WHERE DurableId LIKE '01I%'")
	if err != nil {
		return nil, err
	}
	objName := make(map[string]string, len(objs))
	for _, o := range objs {
		id, api := Field(o, "DurableId"), Field(o, "QualifiedApiName")
		if len(id) < 15 {
			continue
		}
		// Derived entities (__ChangeEvent, __Share, ...) share the object's Id;
		// keep the real object's name.
		if prev, seen := objName[id[:15]]; !seen || (!isObjectName(prev) && isObjectName(api)) {
			objName[id[:15]] = api
		}
	}
	names := make([]string, 0, len(fields))
	for _, f := range fields {
		table := Field(f, "TableEnumOrId")
		if table != "" && table[0] >= '0' && table[0] <= '9' { // an Id, not a standard object name
			n, ok := objName[table[:15]]
			if !ok {
				// EntityDefinition hides some managed/protected objects and
				// we can't know their suffix: skip. ExpandGlobs re-checks a
				// no-match against listMetadata, so this is never fatal.
				continue
			}
			table = n
		}
		names = append(names, table+"."+withNamespace(Field(f, "NamespacePrefix"), Field(f, "DeveloperName"))+"__c")
	}
	return names, nil
}

// isObjectName reports whether an API name ends in a custom-object suffix (as
// opposed to a derived entity such as __ChangeEvent or __Share).
func isObjectName(api string) bool {
	for _, suf := range []string{"__c", "__mdt", "__e", "__b", "__x", "__kav", "__xo"} {
		if strings.HasSuffix(api, suf) {
			return true
		}
	}
	return false
}

// customFieldWhere builds a DeveloperName filter from the field part (after the
// last '.') of every pattern, or "" when any pattern leaves it unconstrained.
// It may over-match (the caller re-filters with the exact glob) but never
// under-matches: namespace prefixes and the "__c" suffix are left out of it.
func customFieldWhere(patterns []string) string {
	var conds []string
	for _, p := range patterns {
		i := strings.LastIndex(p, ".")
		if i < 0 {
			return "" // no object/field split: can't tell which part the pattern constrains
		}
		field := p[i+1:]
		field = strings.TrimSuffix(field, "__c")
		if strings.HasPrefix(field, "*") || strings.Contains(field, "__") {
			return "" // leading wildcard or namespaced field: don't risk narrowing
		}
		like := strings.NewReplacer(`\`, `\\`, `'`, `\'`, "*", "%", "?", "_").Replace(field)
		conds = append(conds, "DeveloperName LIKE '"+strings.TrimSuffix(like, "%")+"%'")
	}
	return strings.Join(conds, " OR ")
}

// listCustomObjects enumerates CustomObject members: every custom object (by
// its 01I… EntityDefinition, excluding derived __ChangeEvent/__Share/... entities)
// plus the customizable standard objects, which listMetadata also returns.
func (c *Client) listCustomObjects(ctx context.Context) ([]string, error) {
	// EntityDefinition rejects OR, so run the two halves separately.
	var names []string
	for _, where := range []string{"DurableId LIKE '01I%'", "IsCustomizable = true"} {
		recs, _, err := c.Query(ctx, "SELECT DurableId, QualifiedApiName FROM EntityDefinition WHERE "+where)
		if err != nil {
			return nil, err
		}
		for _, r := range recs {
			api := Field(r, "QualifiedApiName")
			custom := strings.HasPrefix(Field(r, "DurableId"), "01I")
			if (custom && isObjectName(api)) || (!custom && !strings.Contains(api, "__")) {
				names = append(names, api)
			}
		}
	}
	return names, nil
}
