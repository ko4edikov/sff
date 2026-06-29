// Package project locates and reads an sfdx project (sfdx-project.json) so
// retrieved metadata can be written back in source format at the right place.
package project

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
)

// Dir is one entry of "packageDirectories" in sfdx-project.json.
type Dir struct {
	Path    string `json:"path"`
	Default bool   `json:"default"`
}

// Project is a resolved sfdx project: its root (the dir holding
// sfdx-project.json) and its package directories.
type Project struct {
	Root string
	Dirs []Dir
}

type config struct {
	PackageDirectories []Dir `json:"packageDirectories"`
}

// Find walks up from start looking for an sfdx-project.json and returns the
// project rooted at the first directory that contains one.
func Find(start string) (*Project, error) {
	abs, err := filepath.Abs(start)
	if err != nil {
		return nil, err
	}
	for {
		cfgPath := filepath.Join(abs, "sfdx-project.json")
		if data, err := os.ReadFile(cfgPath); err == nil {
			var c config
			if err := json.Unmarshal(data, &c); err != nil {
				return nil, fmt.Errorf("parse %s: %w", cfgPath, err)
			}
			return &Project{Root: abs, Dirs: c.PackageDirectories}, nil
		}
		parent := filepath.Dir(abs)
		if parent == abs {
			return nil, fmt.Errorf("no sfdx-project.json found from %s upward", start)
		}
		abs = parent
	}
}

// DefaultDir returns the absolute path of the default package directory: the
// first one marked "default", else the first listed, else the project root.
func (p *Project) DefaultDir() string {
	for _, d := range p.Dirs {
		if d.Default {
			return filepath.Join(p.Root, d.Path)
		}
	}
	if len(p.Dirs) > 0 {
		return filepath.Join(p.Root, p.Dirs[0].Path)
	}
	return p.Root
}

// AbsDirs returns the absolute paths of all package directories.
func (p *Project) AbsDirs() []string {
	dirs := make([]string, 0, len(p.Dirs))
	for _, d := range p.Dirs {
		dirs = append(dirs, filepath.Join(p.Root, d.Path))
	}
	return dirs
}
