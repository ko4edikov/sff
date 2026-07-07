package cli

import (
	"context"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/ko4edikov/sff/pkg/auth"
	"github.com/ko4edikov/sff/pkg/mdapi"
	"github.com/ko4edikov/sff/pkg/progress"
	"github.com/ko4edikov/sff/pkg/project"
	"github.com/ko4edikov/sff/pkg/sfapi"
	"github.com/ko4edikov/sff/pkg/source"
)

func newRetrieveCmd() *cobra.Command {
	var metadata []string
	var manifest, outputDir, projectDir, apiVersion string
	var metadataFormat bool
	cmd := &cobra.Command{
		Use:   "retrieve",
		Short: "Retrieve metadata from an org (Metadata API)",
		Long: "Retrieve metadata from an org via the Metadata API, selected by -m Type:Name\n" +
			"specifiers or an existing package.xml. By default the result is converted to\n" +
			"source format and merged into the sfdx project (like sf project retrieve start).\n" +
			"Pass -d to write the converted source into that directory instead of merging;\n" +
			"add --metadata-format to unzip the raw metadata-format files into -d instead.",
		Example: `  sff retrieve -m ApexClass:MyClass
  sff retrieve -m permissionset:Admin,Standard,ReadOnly -o pr-dev
  sff retrieve -m ApexClass -m LWC:myCmp -o pr-dev
  sff retrieve -x manifest/package.xml
  sff retrieve -m ApexClass:MyClass -d ./out
  sff retrieve -m ApexClass:MyClass --metadata-format -d ./mdapi`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			if len(metadata) == 0 && manifest == "" {
				return fmt.Errorf("specify metadata with -m or a manifest with -x")
			}
			// -d only overrides source placement when the user set it; the
			// metadata-format default ("./mdapi") must not leak into source mode.
			sourceDest := ""
			if cmd.Flags().Changed("output-dir") {
				sourceDest = outputDir
			}
			return runRetrieve(cmd.Context(), metadata, manifest, outputDir, sourceDest, projectDir, apiVersion, metadataFormat)
		},
	}
	cmd.Flags().StringArrayVarP(&metadata, "metadata", "m", nil, "metadata to retrieve as Type, Type:Name or Type:Name1,Name2 (case-insensitive, repeatable)")
	cmd.Flags().StringVarP(&manifest, "manifest", "x", "", "path to a package.xml to retrieve")
	cmd.Flags().BoolVar(&metadataFormat, "metadata-format", false, "unzip raw metadata-format files into -d instead of converting to source")
	cmd.Flags().StringVarP(&outputDir, "output-dir", "d", "./mdapi", "output directory: converted source tree, or raw files with --metadata-format")
	cmd.Flags().StringVar(&projectDir, "project-dir", "", "sfdx project to write source into (default: search up from cwd)")
	cmd.Flags().StringVar(&apiVersion, "api-version", sfapi.DefaultAPIVersion, "Metadata API version")
	cmd.MarkFlagsMutuallyExclusive("metadata", "manifest")
	addTargetOrgFlag(cmd)
	return cmd
}

func runRetrieve(ctx context.Context, metadata []string, manifest, outputDir, sourceDest, projectDir, apiVersion string, metadataFormat bool) error {
	org, err := auth.Resolve(targetOrg)
	if err != nil {
		return err
	}

	client := newMDClient(org)
	client.APIVersion = strings.TrimPrefix(apiVersion, "v")

	// The describe catalog powers case-insensitive type resolution below and
	// content/XML-only classification during conversion later; fetch it once
	// (cached, best-effort) and reuse. A failure here is non-fatal — type names
	// fall back to friendly aliases and the converter to its built-in heuristics.
	catalog, _, _ := client.DescribeMetadataCached(ctx, false)

	pkg, err := mdapi.BuildPackage(manifest, metadata, apiVersion, mdapi.NewTypeResolver(catalog))
	if err != nil {
		return err
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

	start := time.Now()
	prog := progress.Start("retrieving")
	res, err := client.RetrieveAndWait(ctx, pkg, func(attempt int) {
		prog.Update(fmt.Sprintf("retrieving (poll %d)", attempt))
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
		fmt.Printf("retrieved %d file(s) to %s (metadata format) in %s\n", len(written), outputDir, fmtDuration(time.Since(start)))
		return nil
	}

	conv, err := source.ConvertZipToSource(res.ZipFile, proj, catalog, sourceDest)
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
	fmt.Printf("retrieved %d file(s) to %s (source format) in %s\n", len(conv.Written), destLabel, fmtDuration(time.Since(start)))
	return nil
}
