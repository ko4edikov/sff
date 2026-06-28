package cli

import (
	"context"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/ko4edikov/sff/internal/auth"
	"github.com/ko4edikov/sff/internal/mdapi"
	"github.com/ko4edikov/sff/internal/project"
	"github.com/ko4edikov/sff/internal/sfapi"
	"github.com/ko4edikov/sff/internal/source"
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
			"source format and merged into the sfdx project (like sf project retrieve start);\n" +
			"use --metadata-format to unzip the raw metadata-format files into -d instead.",
		Example: `  sff retrieve -m ApexClass:MyClass
  sff retrieve -m ApexClass -m LWC:myCmp -o pr-dev
  sff retrieve -x manifest/package.xml
  sff retrieve -m ApexClass:MyClass --metadata-format -d ./mdapi`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			if len(metadata) == 0 && manifest == "" {
				return fmt.Errorf("specify metadata with -m or a manifest with -x")
			}
			return runRetrieve(cmd.Context(), metadata, manifest, outputDir, projectDir, apiVersion, metadataFormat)
		},
	}
	cmd.Flags().StringArrayVarP(&metadata, "metadata", "m", nil, "metadata to retrieve as Type or Type:Name (repeatable)")
	cmd.Flags().StringVarP(&manifest, "manifest", "x", "", "path to a package.xml to retrieve")
	cmd.Flags().BoolVar(&metadataFormat, "metadata-format", false, "unzip raw metadata-format files into -d instead of converting to source")
	cmd.Flags().StringVarP(&outputDir, "output-dir", "d", "./mdapi", "directory for --metadata-format output")
	cmd.Flags().StringVar(&projectDir, "project-dir", "", "sfdx project to write source into (default: search up from cwd)")
	cmd.Flags().StringVar(&apiVersion, "api-version", sfapi.DefaultAPIVersion, "Metadata API version")
	cmd.MarkFlagsMutuallyExclusive("metadata", "manifest")
	addTargetOrgFlag(cmd)
	return cmd
}

func runRetrieve(ctx context.Context, metadata []string, manifest, outputDir, projectDir, apiVersion string, metadataFormat bool) error {
	org, err := auth.Resolve(targetOrg)
	if err != nil {
		return err
	}

	var pkg *mdapi.Package
	if manifest != "" {
		pkg, err = mdapi.LoadManifest(manifest)
	} else {
		pkg, err = mdapi.ParseSpecifiers(metadata, apiVersion)
	}
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

	client := mdapi.New(org)
	client.APIVersion = strings.TrimPrefix(apiVersion, "v")

	start := time.Now()
	res, err := client.RetrieveAndWait(ctx, pkg, func(attempt int) {
		fmt.Fprintf(os.Stderr, "\rretrieving… (poll %d)", attempt)
	})
	fmt.Fprintln(os.Stderr)
	if err != nil {
		return err
	}

	if metadataFormat {
		written, err := mdapi.Unzip(res.ZipFile, outputDir)
		if err != nil {
			return err
		}
		fmt.Printf("retrieved %d file(s) to %s (metadata format) in %s\n", len(written), outputDir, fmtDuration(time.Since(start)))
		return nil
	}

	conv, err := source.ConvertZipToSource(res.ZipFile, proj)
	if err != nil {
		return err
	}
	for _, w := range conv.Warnings {
		fmt.Fprintln(os.Stderr, "warning:", w)
	}
	fmt.Printf("retrieved %d file(s) to %s (source format) in %s\n", len(conv.Written), proj.Root, fmtDuration(time.Since(start)))
	return nil
}
