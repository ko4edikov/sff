package cli

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/spf13/cobra"

	"github.com/ko4edikov/sff/internal/auth"
	"github.com/ko4edikov/sff/internal/sfapi"
	"github.com/ko4edikov/sff/internal/source"
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
	fmt.Fprintf(os.Stderr, "Querying %s from org…\n", t.Name)
	files, err := source.Fetch(ctx, client, t)
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
	return runUnifiedDiff(ctx, remotePath, t.LocalPath, t.Kind != source.Flat)
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

// runUnifiedDiff shells out to `diff -u` (or `-ru` for bundles) and reports
// whether the contents differ. diff exit codes: 0 identical, 1 differences,
// >1 a real error.
func runUnifiedDiff(ctx context.Context, remote, local string, recursive bool) (bool, error) {
	flags := "-u"
	if recursive {
		flags = "-ru"
	}
	cmd := exec.CommandContext(ctx, "diff", flags, remote, local)
	cmd.Stdout, cmd.Stderr = os.Stdout, os.Stderr
	err := cmd.Run()
	if err == nil {
		return false, nil // identical
	}
	if ee, ok := err.(*exec.ExitError); ok && ee.ExitCode() == 1 {
		return true, nil // differences
	}
	return false, fmt.Errorf("diff failed: %w", err)
}
