package source

import (
	"sort"
	"strings"
	"testing"
)

// sampleObject is a CustomObject as the Metadata API pretty-prints it: 4-space
// indent, one element per line.
const sampleObject = `<?xml version="1.0" encoding="UTF-8"?>
<CustomObject xmlns="http://soap.sforce.com/2006/04/metadata">
    <label>Broker</label>
    <fields>
        <fullName>Name__c</fullName>
        <type>Text</type>
        <length>80</length>
    </fields>
    <fields>
        <fullName>Active__c</fullName>
        <type>Checkbox</type>
    </fields>
    <validationRules>
        <fullName>Must_Be_Active</fullName>
        <active>true</active>
        <errorConditionFormula>NOT(Active__c)</errorConditionFormula>
    </validationRules>
</CustomObject>
`

// TestDecomposeRecomposeRoundTrip checks that recompose is the inverse of
// decompose: decomposing a composed object, folding the split files back, and
// decomposing again yields the same parent residual and child files.
func TestDecomposeRecomposeRoundTrip(t *testing.T) {
	co := decompByDir["objects"]
	if co == nil {
		t.Fatal("no decomposition rule for objects")
	}

	parts, err := decompose([]byte(sampleObject), "Broker", co)
	if err != nil {
		t.Fatalf("decompose: %v", err)
	}

	// Build the recompose group from the split files.
	g := &decompGroup{t: co, name: "Broker"}
	for _, p := range parts {
		base := p.rel[strings.LastIndex(p.rel, "/")+1:]
		switch {
		case base == "Broker.object-meta.xml":
			g.parent = p.data
		default:
			child, ok := childForFile(co, base)
			if !ok {
				t.Fatalf("no child rule matched %s", p.rel)
			}
			g.children = append(g.children, decompChildFile{child: child, data: p.data})
		}
	}
	if g.parent == nil {
		t.Fatal("decompose produced no parent residual file")
	}
	if len(g.children) != 3 {
		t.Fatalf("expected 3 child files, got %d", len(g.children))
	}

	recomposed, err := recomposeDecomposed(g)
	if err != nil {
		t.Fatalf("recompose: %v", err)
	}

	// The residual label must survive, and both decomposed element kinds must be
	// folded back at the parent's child indentation.
	rs := string(recomposed)
	for _, want := range []string{
		"    <label>Broker</label>",
		"    <fields>",
		"        <fullName>Name__c</fullName>",
		"    <validationRules>",
		"        <fullName>Must_Be_Active</fullName>",
	} {
		if !strings.Contains(rs, want) {
			t.Errorf("recomposed object missing %q\n---\n%s", want, rs)
		}
	}

	// Idempotence: decomposing the recomposed object reproduces the same files.
	parts2, err := decompose(recomposed, "Broker", co)
	if err != nil {
		t.Fatalf("re-decompose: %v", err)
	}
	if got, want := fileSet(parts2), fileSet(parts); !equalStrings(got, want) {
		t.Errorf("round-trip changed the file set:\n got %v\nwant %v", got, want)
	}
	for _, p2 := range parts2 {
		for _, p1 := range parts {
			if p1.rel == p2.rel && string(p1.data) != string(p2.data) {
				t.Errorf("round-trip changed %s:\n---first---\n%s\n---second---\n%s", p1.rel, p1.data, p2.data)
			}
		}
	}
}

// TestRecomposeSyntheticParent recomposes when no residual parent file exists,
// synthesizing a minimal root around the children.
func TestRecomposeSyntheticParent(t *testing.T) {
	co := decompByDir["objects"]
	child, _ := childForFile(co, "Active__c.field-meta.xml")
	field := []byte(`<?xml version="1.0" encoding="UTF-8"?>
<CustomField xmlns="http://soap.sforce.com/2006/04/metadata">
    <fullName>Active__c</fullName>
    <type>Checkbox</type>
</CustomField>
`)
	g := &decompGroup{t: co, name: "Broker", children: []decompChildFile{{child: child, data: field}}}
	out, err := recomposeDecomposed(g)
	if err != nil {
		t.Fatalf("recompose: %v", err)
	}
	s := string(out)
	if !strings.Contains(s, "<CustomObject xmlns=") || !strings.Contains(s, "    <fields>") || !strings.Contains(s, "        <fullName>Active__c</fullName>") {
		t.Errorf("synthetic recompose wrong:\n%s", s)
	}
}

func fileSet(parts []splitFile) []string {
	s := make([]string, len(parts))
	for i, p := range parts {
		s[i] = p.rel
	}
	sort.Strings(s)
	return s
}

func equalStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
