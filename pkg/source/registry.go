package source

import (
	_ "embed"
	"encoding/json"
	"fmt"
)

// decompositionJSON is the vendored source-format decomposition table, compiled
// into the binary so sff needs no external file at runtime.
//
//go:embed decomposition.json
var decompositionJSON []byte

// DecompType describes how one composed metadata type splits into source files.
type DecompType struct {
	Name          string        `json:"name"`
	DirectoryName string        `json:"directoryName"`
	Suffix        string        `json:"suffix"`
	Layout        string        `json:"layout"` // "folderPerType" or "topLevel"
	Children      []DecompChild `json:"children"`
}

// DecompChild maps a composed child element to its split-file form.
type DecompChild struct {
	XMLTag string `json:"xmlTag"` // element name inside the composed parent file
	Type   string `json:"type"`   // root element of the split child file
	Suffix string `json:"suffix"` // file suffix (before -meta.xml)
}

type decompositionTable struct {
	Types []DecompType `json:"types"`
}

// decompByDir indexes the decomposition table by parent directory name (e.g.
// "objects" → CustomObject).
var decompByDir = mustLoadDecomposition()

func mustLoadDecomposition() map[string]*DecompType {
	var table decompositionTable
	if err := json.Unmarshal(decompositionJSON, &table); err != nil {
		panic(fmt.Sprintf("source: bad embedded decomposition.json: %v", err))
	}
	m := make(map[string]*DecompType, len(table.Types))
	for i := range table.Types {
		t := &table.Types[i]
		m[t.DirectoryName] = t
	}
	return m
}

// childByTag returns the child rule for a composed element tag, if any.
func (t *DecompType) childByTag(tag string) (DecompChild, bool) {
	for _, c := range t.Children {
		if c.XMLTag == tag {
			return c, true
		}
	}
	return DecompChild{}, false
}
