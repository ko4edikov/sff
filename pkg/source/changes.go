package source

import (
	"fmt"
	"path/filepath"
	"sort"
	"strings"

	"github.com/ko4edikov/sff/pkg/mdapi"
	"github.com/ko4edikov/sff/pkg/project"
)

// ChangedFile is one file a change set (e.g. a git diff) touched.
type ChangedFile struct {
	Path    string // absolute path
	Deleted bool   // the file no longer exists
}

// ChangeSet is what to push to an org for a set of changed files: the
// components to deploy and the components to delete (destructiveChanges.xml).
type ChangeSet struct {
	Deploy   *mdapi.Package
	Destroy  *mdapi.Package
	Warnings []string

	// Paths are existing local paths to diff or inspect, one per deployed
	// component: the file itself, or the directory of a bundle (lwc/aura) or
	// static resource. Sorted and de-duplicated.
	Paths []string
}

// ResolveChanges maps changed files to narrow components. A component with a
// surviving file anywhere in the project is deployed (a modified or added file,
// or a deleted file whose component still has other files — e.g. one file of an
// LWC bundle); one with no files left is destroyed. A destroyed decomposed
// parent subsumes its destroyed children, and a deployed parent its deployed
// children. Files outside the package directories or that are not metadata
// components are ignored.
func ResolveChanges(proj *project.Project, files []ChangedFile, version string, catalog *mdapi.DescribeResult) (*ChangeSet, error) {
	byDir := catalogByDir(catalog)
	byType := byTypeName(catalog)
	typeDirs := knownTypeDirs(catalog)
	roots := memberRoots(proj)
	cs := &ChangeSet{}

	present := map[deleteSpec]bool{}
	gone := map[deleteSpec]bool{}
	specPath := map[deleteSpec]string{}
	isBundle := func(folder string, spec deleteSpec) bool {
		o, ok := byDir[folder]
		return bundleDirs[folder] || (ok && o.Suffix == "") || spec.typ == "StaticResource"
	}
	for _, f := range files {
		parts := projectRelParts(proj, f.Path, typeDirs)
		if len(parts) < 2 {
			continue
		}
		spec, ok := classifyDeleteFile(parts, byDir)
		if !ok {
			cs.Warnings = append(cs.Warnings, fmt.Sprintf("%s is not a deployable metadata component; skipped", strings.Join(parts, "/")))
			continue
		}
		rep := f.Path
		bundle := len(parts) > 2 && isBundle(parts[0], spec)
		if bundle { // trim to the bundle / resource directory
			for i := 0; i < len(parts)-2; i++ {
				rep = filepath.Dir(rep)
			}
		}
		if f.Deleted {
			gone[spec] = true
			if bundle {
				specPath[spec] = rep // the directory survives if the component does
			}
		} else {
			present[spec] = true
			specPath[spec] = rep
		}
	}

	destroy := map[deleteSpec]bool{}
	for s := range gone {
		if present[s] {
			continue
		}
		existing, err := resolveSpecFiles(roots, s.typ, s.member, byType)
		if err != nil {
			return nil, err
		}
		if len(existing) > 0 {
			present[s] = true // other files of the component remain: redeploy it
			if specPath[s] == "" {
				specPath[s] = existing[0].path
			}
			continue
		}
		destroy[s] = true
	}
	for s := range destroy {
		if parentSelected(s, destroy) {
			delete(destroy, s)
		}
	}
	deploy := map[deleteSpec]bool{}
	for s := range present {
		if !parentSelected(s, present) {
			deploy[s] = true
		}
	}

	seenPath := map[string]bool{}
	for s := range deploy {
		if p := specPath[s]; p != "" && !seenPath[p] {
			seenPath[p] = true
			cs.Paths = append(cs.Paths, p)
		}
	}
	sort.Strings(cs.Paths)
	cs.Deploy = specsToPackage(deploy, version)
	cs.Destroy = specsToPackage(destroy, version)
	sort.Strings(cs.Warnings)
	return cs, nil
}

func specsToPackage(specs map[deleteSpec]bool, version string) *mdapi.Package {
	byType := map[string][]string{}
	for s := range specs {
		byType[s.typ] = append(byType[s.typ], s.member)
	}
	pkg := &mdapi.Package{Version: strings.TrimPrefix(version, "v")}
	types := make([]string, 0, len(byType))
	for t := range byType {
		types = append(types, t)
	}
	sort.Strings(types)
	for _, t := range types {
		sort.Strings(byType[t])
		pkg.Types = append(pkg.Types, mdapi.PackageTypes{Name: t, Members: byType[t]})
	}
	return pkg
}
