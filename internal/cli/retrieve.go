package cli

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/ko4edikov/sff/pkg/auth"
	"github.com/ko4edikov/sff/pkg/mdapi"
	"github.com/ko4edikov/sff/pkg/progress"
	"github.com/ko4edikov/sff/pkg/project"
	"github.com/ko4edikov/sff/pkg/source"
)

func newRetrieveCmd() *cobra.Command {
	var metadata []string
	var manifest, outputDir, projectDir, apiVersion string
	var metadataFormat, dryRun bool
	var filter modFilter
	cmd := &cobra.Command{
		Use:   "retrieve",
		Short: "Retrieve metadata from an org (Metadata API)",
		Long: "Retrieve metadata from an org via the Metadata API, selected by -m Type:Name\n" +
			"specifiers or an existing package.xml. By default the result is converted to\n" +
			"source format and merged into the sfdx project (like sf project retrieve start).\n" +
			"Pass -d to write the converted source into that directory instead of merging;\n" +
			"add --metadata-format to unzip the raw metadata-format files into -d instead.\n\n" +
			"--since/--until/--modified-by narrow the selection by modification time and\n" +
			"author (applied on top of -m/-x; on a sandbox -m may be omitted to retrieve\n" +
			"everything that changed, via source tracking). Components from installed\n" +
			"packages are skipped unless --include-packages. Use --dry-run to list the\n" +
			"resolved components without retrieving; see also `sff metadata list`.",
		Example: `  sff retrieve -m ApexClass:MyClass
  sff retrieve -m permissionset:Admin,Standard,ReadOnly -o pr-dev
  sff retrieve -m ApexClass -m LWC:myCmp -o pr-dev
  sff retrieve -x manifest/package.xml
  sff retrieve -m ApexClass:MyClass -d ./out
  sff retrieve -m ApexClass:MyClass --metadata-format -d ./mdapi
  sff retrieve -m ApexClass -m LWC --since 2d --modified-by me
  sff retrieve --since 1d --modified-by "Jane*" -o pr-dev --dry-run`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			if len(metadata) == 0 && manifest == "" && !filter.hasChangeFilter() {
				return fmt.Errorf("specify metadata with -m or a manifest with -x (or --since/--modified-by on a sandbox)")
			}
			// -d only overrides source placement when the user set it; the
			// metadata-format default ("./mdapi") must not leak into source mode.
			sourceDest := ""
			if cmd.Flags().Changed("output-dir") {
				sourceDest = outputDir
			}
			return runRetrieve(cmd.Context(), metadata, manifest, outputDir, sourceDest, projectDir, apiVersion, metadataFormat, &filter, dryRun)
		},
	}
	cmd.Flags().StringArrayVarP(&metadata, "metadata", "m", nil, "metadata to retrieve as Type, Type:Name or Type:Name1,Name2; names may use * and ? patterns, e.g. CustomObject:EUR_Score* (case-insensitive, repeatable)")
	cmd.Flags().StringVarP(&manifest, "manifest", "x", "", "path to a package.xml to retrieve")
	cmd.Flags().BoolVar(&metadataFormat, "metadata-format", false, "unzip raw metadata-format files into -d instead of converting to source")
	cmd.Flags().StringVarP(&outputDir, "output-dir", "d", "./mdapi", "output directory: converted source tree, or raw files with --metadata-format")
	cmd.Flags().StringVar(&projectDir, "project-dir", "", "sfdx project to write source into (default: search up from cwd)")
	cmd.Flags().StringVar(&apiVersion, "api-version", "", apiVersionUsage)
	cmd.Flags().BoolVar(&dryRun, "dry-run", false, "list the components that would be retrieved, without retrieving")
	addModFilterFlags(cmd, &filter)
	cmd.MarkFlagsMutuallyExclusive("metadata", "manifest")
	addTargetOrgFlag(cmd)
	return cmd
}

func runRetrieve(ctx context.Context, metadata []string, manifest, outputDir, sourceDest, projectDir, apiVersion string, metadataFormat bool, filter *modFilter, dryRun bool) error {
	start := time.Now()
	// One spinner spans the whole pre-retrieve prep so a slow step (token
	// refresh, API-version lookup, describe, listMetadata for pattern
	// expansion) is visible instead of a silent pause.
	prog := progress.StartAt(start, "resolving org")
	defer prog.Stop() // idempotent; covers early error returns

	org, err := auth.Resolve(targetOrg)
	if err != nil {
		return err
	}
	prog.Update("resolving API version (" + org.Username + ")")
	apiVersion = resolveAPIVersion(ctx, apiVersion, projectDir, org)

	client := newMDClient(org)
	client.APIVersion = strings.TrimPrefix(apiVersion, "v")
	client.OnStage = prog.Update

	// The describe catalog powers case-insensitive type resolution below and
	// content/XML-only classification during conversion later; fetch it once
	// (cached, best-effort) and reuse. A failure here is non-fatal — type names
	// fall back to friendly aliases and the converter to its built-in heuristics.
	prog.Update("loading metadata describe")
	catalog, _, _ := client.DescribeMetadataCached(ctx, false)

	var pkg *mdapi.Package
	if len(metadata) > 0 || manifest != "" {
		if pkg, err = mdapi.BuildPackage(manifest, metadata, apiVersion, mdapi.NewTypeResolver(catalog)); err != nil {
			return err
		}
	}
	if filter.active() {
		// Filters resolve the exact components (so patterns in -m are matched
		// there rather than via ExpandGlobs).
		client.OnStage = prog.Update
		sel, err := resolveModified(ctx, org, client, catalog, pkg, filter, apiVersion, prog.Update)
		if err != nil {
			return err
		}
		prog.Stop()
		reportHidden(os.Stderr, sel.Hidden)
		reportSkipped(os.Stderr, sel.Skipped)
		if len(sel.Entries) == 0 {
			fmt.Fprintf(os.Stderr, "no components match the filters (checked via %s)\n", sel.Source)
			return nil
		}
		if dryRun {
			return printEntries(os.Stdout, sel.Entries, false)
		}
		prog = progress.StartAt(start, "retrieving")
		pkg = packageFromEntries(sel.Entries, apiVersion)
	} else {
		if err := client.ExpandGlobs(ctx, pkg); err != nil {
			return err
		}
		if dryRun {
			prog.Stop()
			return printEntries(os.Stdout, entriesFromPackage(pkg), false)
		}
	}

	// Resolve the source-format destination project up front so a missing
	// project fails before we spend time on the retrieve.
	var proj *project.Project
	if !metadataFormat {
		start := projectDir
		if start == "" {
			start = "."
		}
		proj, err = project.Find(start)
		if err != nil {
			return fmt.Errorf("%w; use --metadata-format to retrieve without a project", err)
		}
	}

	nMembers := 0
	for _, t := range pkg.Types {
		nMembers += len(t.Members)
	}
	prog.Update(fmt.Sprintf("retrieving %d component(s) in %d type(s) from %s", nMembers, len(pkg.Types), org.Username))
	res, err := client.RetrieveAndWait(ctx, pkg, func(attempt int) {
		prog.Update(fmt.Sprintf("retrieving %d component(s) (poll %d)", nMembers, attempt))
	})
	prog.Stop()
	if err != nil {
		return err
	}

	// Surface the org's retrieve messages (e.g. a requested component that
	// couldn't be found) even on success — otherwise a typo'd or missing member
	// silently yields a package.xml-only result.
	for _, m := range res.Messages {
		fmt.Fprintln(os.Stderr, "warning:", m)
	}

	if metadataFormat {
		written, err := mdapi.Unzip(res.ZipFile, outputDir)
		if err != nil {
			return err
		}
		printFileList(written, outputDir)
		fmt.Printf("retrieved %d file(s) to %s (metadata format) in %s\n", len(written), outputDir, fmtDuration(time.Since(start)))
		return nil
	}

	conv, err := source.ConvertZipToSource(res.ZipFile, proj, catalog, sourceDest, nil)
	if err != nil {
		return err
	}
	for _, w := range conv.Warnings {
		fmt.Fprintln(os.Stderr, "warning:", w)
	}
	destLabel := proj.Root
	if sourceDest != "" {
		destLabel = sourceDest
	}
	printFileList(conv.Paths, "")
	fmt.Printf("retrieved %d file(s) to %s (source format) in %s\n", len(conv.Written), destLabel, fmtDuration(time.Since(start)))
	return nil
}

// printFileList prints the retrieved files, sorted, one per line. Absolute paths
// are shown relative to the current directory when they sit under it; names
// already relative (a zip listing) are joined onto base when one is given.
func printFileList(files []string, base string) {
	cwd, _ := os.Getwd()
	out := make([]string, 0, len(files))
	for _, f := range files {
		if base != "" && !filepath.IsAbs(f) {
			f = filepath.Join(base, f)
		}
		if filepath.IsAbs(f) && cwd != "" {
			if rel, err := filepath.Rel(cwd, f); err == nil && !strings.HasPrefix(rel, "..") {
				f = rel
			}
		}
		out = append(out, f)
	}
	sort.Strings(out)
	for _, f := range out {
		fmt.Println("  " + f)
	}
}

// entriesFromPackage flattens a manifest into selection rows (no modification
// details are known for an explicit package).
func entriesFromPackage(pkg *mdapi.Package) []modEntry {
	var out []modEntry
	for _, t := range pkg.Types {
		for _, m := range t.Members {
			out = append(out, modEntry{Type: t.Name, Name: m})
		}
	}
	return out
}
