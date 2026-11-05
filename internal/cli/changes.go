package cli

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/ko4edikov/sff/pkg/gitchange"
	"github.com/ko4edikov/sff/pkg/mdapi"
	"github.com/ko4edikov/sff/pkg/project"
	"github.com/ko4edikov/sff/pkg/source"
)

// fromGit reports whether the selection is derived from git changes
// (--changed or --commit) rather than from -d/-m/-x.
func (s deploySelection) fromGit() bool { return s.changed || s.commit != "" }

// resolveGitChanges lists the files changed in the working tree (vs HEAD) or in
// a commit and maps them to components to deploy and to destroy. The returned
// project is the one the files were resolved against. Warnings are printed.
func resolveGitChanges(sel deploySelection, version string, catalog *mdapi.DescribeResult) (*source.ChangeSet, *project.Project, error) {
	start := sel.projectDir
	if start == "" {
		start = "."
	}
	proj, err := project.Find(start)
	if err != nil {
		return nil, nil, err
	}

	var files []gitchange.File
	if sel.commit != "" {
		files, err = gitchange.Commit(proj.Root, sel.commit)
	} else {
		files, err = gitchange.Working(proj.Root)
	}
	if err != nil {
		return nil, nil, err
	}
	changed := make([]source.ChangedFile, 0, len(files))
	for _, f := range files {
		cf := source.ChangedFile{Path: f.Path, Deleted: f.Deleted}
		if strings.HasSuffix(f.Path, "/labels/CustomLabels.labels-meta.xml") {
			cf.Old, _ = gitchange.ShowOld(proj.Root, sel.commit, f.Path)
		}
		changed = append(changed, cf)
	}

	cs, err := source.ResolveChanges(proj, changed, version, catalog)
	if err != nil {
		return nil, nil, err
	}
	for _, w := range cs.Warnings {
		fmt.Fprintln(os.Stderr, "warning:", w)
	}
	if componentCount(cs.Deploy) == 0 && componentCount(cs.Destroy) == 0 {
		return nil, nil, fmt.Errorf("no metadata changes found under %s", filepath.Base(proj.Root))
	}
	return cs, proj, nil
}

// recomposeChangeSet recomposes the changed components to deploy into
// metadata-format entries. With nothing to deploy (deletions only) it returns an
// empty result so the caller can still send the destructive manifest.
func recomposeChangeSet(proj *project.Project, cs *source.ChangeSet, version string, catalog *mdapi.DescribeResult) (*source.RecomposeResult, error) {
	if componentCount(cs.Deploy) == 0 {
		return &source.RecomposeResult{Entries: map[string][]byte{}, Package: cs.Deploy}, nil
	}
	return source.RecomposeMembers(proj, cs.Deploy, version, catalog)
}
