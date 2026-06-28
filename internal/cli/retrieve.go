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
	"github.com/ko4edikov/sff/internal/sfapi"
)

func newRetrieveCmd() *cobra.Command {
	var metadata []string
	var manifest, outputDir, apiVersion string
	cmd := &cobra.Command{
		Use:   "retrieve",
		Short: "Retrieve metadata from an org (Metadata API)",
		Long:  "Retrieve metadata-format files from an org via the Metadata API, selected by\n-m Type:Name specifiers or an existing package.xml.",
		Example: `  sff retrieve -m ApexClass:MyClass -d ./out
  sff retrieve -m ApexClass -m LWC:myCmp -o pr-dev
  sff retrieve -x manifest/package.xml -d ./out`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			if len(metadata) == 0 && manifest == "" {
				return fmt.Errorf("specify metadata with -m or a manifest with -x")
			}
			return runRetrieve(cmd.Context(), metadata, manifest, outputDir, apiVersion)
		},
	}
	cmd.Flags().StringArrayVarP(&metadata, "metadata", "m", nil, "metadata to retrieve as Type or Type:Name (repeatable)")
	cmd.Flags().StringVarP(&manifest, "manifest", "x", "", "path to a package.xml to retrieve")
	cmd.Flags().StringVarP(&outputDir, "output-dir", "d", "./mdapi", "directory to unzip the retrieved metadata into")
	cmd.Flags().StringVar(&apiVersion, "api-version", sfapi.DefaultAPIVersion, "Metadata API version")
	cmd.MarkFlagsMutuallyExclusive("metadata", "manifest")
	return cmd
}

func runRetrieve(ctx context.Context, metadata []string, manifest, outputDir, apiVersion string) error {
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

	written, err := mdapi.Unzip(res.ZipFile, outputDir)
	if err != nil {
		return err
	}
	fmt.Printf("retrieved %d file(s) to %s in %s\n", len(written), outputDir, fmtDuration(time.Since(start)))
	return nil
}
