package cli

import (
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/ko4edikov/sff/pkg/auth"
	"github.com/ko4edikov/sff/pkg/mdapi"
	"github.com/ko4edikov/sff/pkg/progress"
)

func newMetadataCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "metadata",
		Short: "Inspect metadata in an org",
	}
	cmd.AddCommand(newMetadataListCmd())
	return cmd
}

// newMetadataListCmd lists components with who/when last modified them. It takes
// the same -m/-x selection and --since/--until/--modified-by filters as
// `sff retrieve`, so what it prints is what `retrieve` would fetch.
func newMetadataListCmd() *cobra.Command {
	var metadata []string
	var manifest, apiVersion string
	var asJSON, asManifest bool
	var filter modFilter
	cmd := &cobra.Command{
		Use:   "list",
		Short: "List org metadata with last-modified details (filterable by time and author)",
		Long: "List the components selected by -m Type[:Name] / -x (names may use * and ?\n" +
			"patterns) together with who last modified them and when. --since, --until and\n" +
			"--modified-by filter the list exactly as they do for `sff retrieve`; on a\n" +
			"sandbox -m may be omitted to list everything that changed (via source\n" +
			"tracking). Output is a table, --json, or a package.xml with --package-xml that\n" +
			"can be fed straight to `sff retrieve -x`.",
		Example: `  sff metadata list -m ApexClass --since 3d
  sff metadata list --since 1d --modified-by me -o pr-dev
  sff metadata list -m customfield:'Account.*' --modified-by "Jane*" --json
  sff metadata list --since 2d --package-xml > package.xml`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			if len(metadata) == 0 && manifest == "" && !filter.hasChangeFilter() {
				return fmt.Errorf("specify metadata with -m or a manifest with -x (or --since/--modified-by on a sandbox)")
			}
			ctx := cmd.Context()
			org, err := auth.Resolve(targetOrg)
			if err != nil {
				return err
			}
			prog := progress.Start("resolving org")
			defer prog.Stop()
			apiVersion = resolveAPIVersion(ctx, apiVersion, "", org)
			client := newMDClient(org)
			client.APIVersion = strings.TrimPrefix(apiVersion, "v")
			prog.Update("loading metadata describe")
			catalog, _, _ := client.DescribeMetadataCached(ctx, false)

			var scope *mdapi.Package
			if len(metadata) > 0 || manifest != "" {
				if scope, err = mdapi.BuildPackage(manifest, metadata, apiVersion, mdapi.NewTypeResolver(catalog)); err != nil {
					return err
				}
			}
			start := time.Now()
			sel, err := resolveModified(ctx, org, client, catalog, scope, &filter, apiVersion, prog.Update)
			prog.Stop()
			if err != nil {
				return err
			}
			reportHidden(os.Stderr, sel.Hidden)
			reportSkipped(os.Stderr, sel.Skipped)
			if asManifest {
				xml, err := packageFromEntries(sel.Entries, apiVersion).XML()
				if err != nil {
					return err
				}
				_, err = os.Stdout.Write(xml)
				return err
			}
			if err := printEntries(os.Stdout, sel.Entries, asJSON); err != nil {
				return err
			}
			if !asJSON {
				fmt.Fprintf(os.Stderr, "%d component(s) via %s in %s\n", len(sel.Entries), sel.Source, fmtDuration(time.Since(start)))
			}
			return nil
		},
	}
	cmd.Flags().StringArrayVarP(&metadata, "metadata", "m", nil, "metadata to list as Type, Type:Name or Type:Name1,Name2; names may use * and ? patterns (repeatable)")
	cmd.Flags().StringVarP(&manifest, "manifest", "x", "", "path to a package.xml selecting what to list")
	cmd.Flags().StringVar(&apiVersion, "api-version", "", apiVersionUsage)
	cmd.Flags().BoolVar(&asJSON, "json", false, "print the list as JSON")
	cmd.Flags().BoolVar(&asManifest, "package-xml", false, "print the selection as a package.xml instead of a table")
	addModFilterFlags(cmd, &filter)
	cmd.MarkFlagsMutuallyExclusive("metadata", "manifest")
	cmd.MarkFlagsMutuallyExclusive("json", "package-xml")
	addTargetOrgFlag(cmd)
	return cmd
}
