// Package gitchange lists the files a git working tree or commit changed, by
// shelling out to the git binary (no go-git dependency).
package gitchange

import (
	"fmt"
	"os/exec"
	"path/filepath"
	"strings"
)

// File is one changed path. A rename is reported as the old path with Deleted
// set plus the new path without it.
type File struct {
	Path    string // absolute path
	Deleted bool   // the file no longer exists at the target state
}

// Working returns the changes in dir's working tree relative to HEAD: staged,
// unstaged and untracked (but not ignored) files. In a repository with no
// commits yet, every tracked or untracked file counts as added.
func Working(dir string) ([]File, error) {
	top, err := toplevel(dir)
	if err != nil {
		return nil, err
	}
	var files []File
	if git(top, "rev-parse", "--verify", "-q", "HEAD") == nil {
		out, err := gitOut(top, "diff", "--name-status", "-M", "-z", "HEAD")
		if err != nil {
			return nil, err
		}
		if files, err = parseNameStatus(top, out); err != nil {
			return nil, err
		}
		untracked, err := gitOut(top, "ls-files", "-z", "--others", "--exclude-standard")
		if err != nil {
			return nil, err
		}
		files = append(files, listFiles(top, untracked)...)
		return files, nil
	}
	out, err := gitOut(top, "ls-files", "-z", "--cached", "--others", "--exclude-standard")
	if err != nil {
		return nil, err
	}
	return listFiles(top, out), nil
}

// Commit returns the changes ref introduced relative to its first parent (or
// relative to the empty tree for a root commit).
func Commit(dir, ref string) ([]File, error) {
	top, err := toplevel(dir)
	if err != nil {
		return nil, err
	}
	if err := git(top, "rev-parse", "--verify", "-q", ref+"^{commit}"); err != nil {
		return nil, fmt.Errorf("%q is not a commit", ref)
	}
	var out []byte
	if git(top, "rev-parse", "--verify", "-q", ref+"^") == nil {
		out, err = gitOut(top, "diff", "--name-status", "-M", "-z", ref+"^", ref)
	} else {
		out, err = gitOut(top, "diff-tree", "-r", "--root", "--name-status", "-M", "-z", "--no-commit-id", ref)
	}
	if err != nil {
		return nil, err
	}
	return parseNameStatus(top, out)
}

// parseNameStatus decodes `git diff --name-status -z` output: a status token
// followed by one path (two for renames and copies).
func parseNameStatus(top string, out []byte) ([]File, error) {
	toks := strings.Split(strings.TrimRight(string(out), "\x00"), "\x00")
	var files []File
	for i := 0; i < len(toks) && toks[i] != ""; {
		status := toks[i]
		switch status[0] {
		case 'R', 'C':
			if i+2 >= len(toks) {
				return nil, fmt.Errorf("malformed git name-status output")
			}
			if status[0] == 'R' {
				files = append(files, File{Path: filepath.Join(top, toks[i+1]), Deleted: true})
			}
			files = append(files, File{Path: filepath.Join(top, toks[i+2])})
			i += 3
		default:
			if i+1 >= len(toks) {
				return nil, fmt.Errorf("malformed git name-status output")
			}
			files = append(files, File{Path: filepath.Join(top, toks[i+1]), Deleted: status[0] == 'D'})
			i += 2
		}
	}
	return files, nil
}

func listFiles(top string, out []byte) []File {
	var files []File
	for _, p := range strings.Split(string(out), "\x00") {
		if p != "" {
			files = append(files, File{Path: filepath.Join(top, p)})
		}
	}
	return files
}

func toplevel(dir string) (string, error) {
	out, err := gitOut(dir, "rev-parse", "--show-toplevel")
	if err != nil {
		return "", fmt.Errorf("%s is not inside a git repository", dir)
	}
	return strings.TrimSpace(string(out)), nil
}

func git(dir string, args ...string) error {
	_, err := gitOut(dir, args...)
	return err
}

func gitOut(dir string, args ...string) ([]byte, error) {
	out, err := exec.Command("git", append([]string{"-C", dir}, args...)...).Output()
	if err != nil {
		return nil, fmt.Errorf("git %s: %w", strings.Join(args, " "), err)
	}
	return out, nil
}
