package cli

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"

	"github.com/pmezard/go-difflib/difflib"
	"github.com/spf13/cobra"

	"github.com/ko4edikov/sff/pkg/auth"
	"github.com/ko4edikov/sff/pkg/sfapi"
	"github.com/ko4edikov/sff/pkg/source"
)

func newDiffCmd() *cobra.Command {
	var execTmpl, apiVersion string
	cmd := &cobra.Command{
		Use:   "diff <file|name>...",
		Short: "Diff local metadata against the org",
		Long: "Fetch one or more components' source from the org (Tooling API) and compare\n" +
			"with the local copy. Supports Apex (.cls/.trigger/.page/.component) and LWC/Aura\n" +
			"bundles. A directory argument is walked recursively for all supported metadata.\n\n" +
			"Viewer: --exec '<tmpl>' takes precedence, then $SFF_DIFF; both use {remote}\n" +
			"and {local} placeholders. With neither, a unified diff is printed to stdout\n" +
			"and the exit code is 1 when any target differs.",
		Example: `  sff diff MyClass
  sff diff MyClass OtherClass lwc/myCmp -o pr-dev
  SFF_DIFF='idea diff {remote} {local}' sff diff MyClass
  sff diff MyClass --exec 'code --diff {remote} {local}'`,
		Args: cobra.MinimumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return runDiff(cmd.Context(), args, execTmpl, apiVersion)
		},
	}
	cmd.Flags().StringVar(&execTmpl, "exec", "", "diff viewer command template using {remote}/{local}")
	cmd.Flags().StringVar(&apiVersion, "api-version", sfapi.DefaultAPIVersion, "Tooling API version")
	addTargetOrgFlag(cmd)
	return cmd
}

func runDiff(ctx context.Context, args []string, execTmpl, apiVersion string) error {
	org, err := auth.Resolve(targetOrg)
	if err != nil {
		return err
	}
	client := sfapi.New(org)
	client.APIVersion = apiVersion

	viewer := execTmpl
	if viewer == "" {
		viewer = os.Getenv("SFF_DIFF")
	}

	// Expand each arg (a file, a bundle, or a directory) into concrete targets.
	var targets []*source.Target
	failed := false
	for _, arg := range args {
		ts, err := source.ResolveAll(arg)
		if err != nil {
			fmt.Fprintf(os.Stderr, "sff: %s: %v\n", arg, err)
			failed = true
			continue
		}
		targets = append(targets, ts...)
	}

	multi := len(targets) > 1
	anyDiff := false
	for _, t := range targets {
		if multi {
			fmt.Fprintf(os.Stderr, "\n=== %s ===\n", t.Name)
		}
		differed, err := diffTarget(ctx, client, t, viewer)
		if err != nil {
			// Report and continue so one bad target doesn't abort the batch.
			fmt.Fprintf(os.Stderr, "sff: %s: %v\n", t.Name, err)
			failed = true
			continue
		}
		anyDiff = anyDiff || differed
	}
	if failed || anyDiff {
		return &ExitError{Code: 1}
	}
	return nil
}

// diffTarget fetches and diffs a single resolved target, returning whether it
// differs (only meaningful for the built-in unified-diff fallback; viewer mode
// reports false).
func diffTarget(ctx context.Context, client *sfapi.Client, t *source.Target, viewer string) (bool, error) {
	prog := startProgress(fmt.Sprintf("querying %s from org", t.Name))
	files, err := source.Fetch(ctx, client, t)
	prog.Stop()
	if err != nil {
		return false, err
	}
	remotePath, err := writeRemote(t, files)
	if err != nil {
		return false, err
	}
	fmt.Fprintf(os.Stderr, "✓ %d file(s) — diffing…\n", len(files))

	if viewer != "" {
		return false, runViewer(ctx, viewer, remotePath, t.LocalPath)
	}
	differed, err := runUnifiedDiff(remotePath, t.LocalPath, t.Kind != source.Flat)
	if err == nil && !differed {
		fmt.Fprintf(os.Stderr, "✓ %s: no differences\n", t.Name)
	}
	return differed, err
}

// writeRemote materializes the org source under a temp dir and returns the path
// to diff against (a file for flat, a directory for a bundle).
func writeRemote(t *source.Target, files []source.RemoteFile) (string, error) {
	base := filepath.Join(os.TempDir(), "sff-diff")

	if t.Kind == source.Flat {
		ext := filepath.Ext(t.LocalPath)
		remote := filepath.Join(base, t.Name+".org"+ext)
		local, _ := os.ReadFile(t.LocalPath)
		if err := writeFile(remote, source.Normalize(files[0].Content, local)); err != nil {
			return "", err
		}
		return remote, nil
	}

	remoteDir := filepath.Join(base, t.Name+".org")
	if err := os.RemoveAll(remoteDir); err != nil {
		return "", err
	}
	for _, f := range files {
		local, _ := os.ReadFile(filepath.Join(t.LocalPath, f.Rel))
		if err := writeFile(filepath.Join(remoteDir, f.Rel), source.Normalize(f.Content, local)); err != nil {
			return "", err
		}
	}
	return remoteDir, nil
}

func writeFile(path string, data []byte) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	return os.WriteFile(path, data, 0o644)
}

// runViewer launches a configured diff tool, substituting {remote}/{local}.
func runViewer(ctx context.Context, tmpl, remote, local string) error {
	fields := strings.Fields(tmpl)
	if len(fields) == 0 {
		return fmt.Errorf("empty diff viewer command")
	}
	repl := strings.NewReplacer("{remote}", remote, "{local}", local)
	for i, f := range fields {
		fields[i] = repl.Replace(f)
	}
	cmd := exec.CommandContext(ctx, fields[0], fields[1:]...)
	cmd.Stdin, cmd.Stdout, cmd.Stderr = os.Stdin, os.Stdout, os.Stderr
	if err := cmd.Run(); err != nil {
		// A non-zero exit from an interactive viewer (e.g. quitting nvim -d) is
		// not an error for us; only a failure to launch is.
		if _, ok := err.(*exec.ExitError); ok {
			return nil
		}
		return fmt.Errorf("run diff viewer %q: %w", fields[0], err)
	}
	return nil
}

// runUnifiedDiff produces a unified diff in-process (no external `diff`
// binary, so it behaves identically on every OS), prints it colorized, and
// reports whether the contents differ. recursive diffs a bundle directory pair;
// otherwise it diffs two files.
func runUnifiedDiff(remote, local string, recursive bool) (bool, error) {
	if recursive {
		return diffTrees(remote, local)
	}
	return diffFiles(remote, local)
}

// diffFiles writes a unified diff of two files (a missing side is treated as
// empty) and reports whether they differ.
func diffFiles(remote, local string) (bool, error) {
	rb, err := readOrEmpty(remote)
	if err != nil {
		return false, err
	}
	lb, err := readOrEmpty(local)
	if err != nil {
		return false, err
	}
	text, err := difflib.GetUnifiedDiffString(difflib.UnifiedDiff{
		A:        toLines(rb),
		B:        toLines(lb),
		FromFile: remote,
		FromDate: modTime(remote),
		ToFile:   local,
		ToDate:   modTime(local),
		Context:  3,
	})
	if err != nil {
		return false, fmt.Errorf("diff %s: %w", local, err)
	}
	if text == "" {
		return false, nil
	}
	writeColorDiff(os.Stdout, []byte(text))
	return true, nil
}

// diffTrees diffs every file under two bundle directories (paired by relative
// path; a file present on only one side diffs against an empty counterpart) and
// reports whether anything differs.
func diffTrees(remote, local string) (bool, error) {
	rels := map[string]bool{}
	for _, root := range []string{remote, local} {
		_ = filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
			if err != nil || d.IsDir() {
				return nil
			}
			rel, rerr := filepath.Rel(root, p)
			if rerr == nil {
				rels[filepath.ToSlash(rel)] = true
			}
			return nil
		})
	}
	ordered := make([]string, 0, len(rels))
	for rel := range rels {
		ordered = append(ordered, rel)
	}
	sort.Strings(ordered)

	differed := false
	for _, rel := range ordered {
		fp := filepath.FromSlash(rel)
		d, err := diffFiles(filepath.Join(remote, fp), filepath.Join(local, fp))
		if err != nil {
			return differed, err
		}
		differed = differed || d
	}
	return differed, nil
}

// readOrEmpty reads a file, returning nil bytes (not an error) when it is absent.
func readOrEmpty(path string) ([]byte, error) {
	b, err := os.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	return b, err
}

// toLines splits content into newline-terminated lines for difflib, mapping
// empty content to no lines (so it diffs cleanly against a present file).
func toLines(b []byte) []string {
	if len(b) == 0 {
		return nil
	}
	return difflib.SplitLines(string(b))
}

// modTime returns a file's modification time as a diff header date, or "".
func modTime(path string) string {
	fi, err := os.Stat(path)
	if err != nil {
		return ""
	}
	return fi.ModTime().Format("2006-01-02 15:04:05")
}

// colorSupported is set once at startup: it enables ANSI processing on the
// Windows console (no-op elsewhere) and records whether colors can render.
var colorSupported = enableVirtualTerminal()

// ANSI colors for the built-in unified diff.
const (
	ansiReset = "\x1b[0m"
	ansiRed   = "\x1b[31m"
	ansiGreen = "\x1b[32m"
	ansiCyan  = "\x1b[36m"
	ansiBold  = "\x1b[1m"
)

// writeColorDiff prints a unified diff to w, coloring it like git when w is a
// terminal (added green, removed red, hunks cyan, file headers bold). Output is
// left plain when piped/redirected or when NO_COLOR is set, keeping it clean for
// scripts.
func writeColorDiff(w io.Writer, data []byte) {
	color := colorSupported && isTerminal(w) && os.Getenv("NO_COLOR") == ""
	sc := bufio.NewScanner(bytes.NewReader(data))
	sc.Buffer(make([]byte, 0, 64*1024), 16*1024*1024) // tolerate long lines
	for sc.Scan() {
		line := sc.Text()
		if !color {
			fmt.Fprintln(w, line)
			continue
		}
		switch {
		case strings.HasPrefix(line, "+++"), strings.HasPrefix(line, "---"),
			strings.HasPrefix(line, "diff "):
			fmt.Fprintln(w, ansiBold+line+ansiReset)
		case strings.HasPrefix(line, "@@"):
			fmt.Fprintln(w, ansiCyan+line+ansiReset)
		case strings.HasPrefix(line, "+"):
			fmt.Fprintln(w, ansiGreen+line+ansiReset)
		case strings.HasPrefix(line, "-"):
			fmt.Fprintln(w, ansiRed+line+ansiReset)
		default:
			fmt.Fprintln(w, line)
		}
	}
}

// isTerminal reports whether w is a character device (a TTY).
func isTerminal(w io.Writer) bool {
	f, ok := w.(*os.File)
	if !ok {
		return false
	}
	fi, err := f.Stat()
	return err == nil && fi.Mode()&os.ModeCharDevice != 0
}
