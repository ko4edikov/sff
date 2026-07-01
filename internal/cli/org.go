package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"os"
	"os/exec"
	"runtime"
	"strings"
	"text/tabwriter"

	"github.com/spf13/cobra"

	"github.com/ko4edikov/sff/pkg/auth"
	"github.com/ko4edikov/sff/pkg/mdapi"
	"github.com/ko4edikov/sff/pkg/sfapi"
)

func newOrgCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "org",
		Short: "Inspect stored orgs",
	}
	cmd.AddCommand(newOrgDisplayCmd())
	cmd.AddCommand(newOrgListCmd())
	cmd.AddCommand(newOrgOpenCmd())
	return cmd
}

func newOrgListCmd() *cobra.Command {
	var asJSON bool
	cmd := &cobra.Command{
		Use:   "list",
		Short: "List authenticated orgs (reads sf's ~/.sfdx)",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			orgs, err := auth.ListOrgs()
			if err != nil {
				return err
			}
			if asJSON {
				enc := json.NewEncoder(os.Stdout)
				enc.SetIndent("", "  ")
				return enc.Encode(orgs)
			}
			return printOrgs(orgs)
		},
	}
	cmd.Flags().BoolVar(&asJSON, "json", false, "output JSON")
	cmd.AddCommand(newOrgListMetadataTypesCmd())
	return cmd
}

func newOrgListMetadataTypesCmd() *cobra.Command {
	var asJSON, refresh bool
	var apiVersion string
	cmd := &cobra.Command{
		Use:     "metadata-types",
		Short:   "List the org's metadata types (Metadata API describeMetadata)",
		Long:    "Call describeMetadata and print the metadata type catalog. Results are cached\nin ~/.sff keyed by org and API version; use --refresh to re-fetch.",
		Aliases: []string{"metadata-type"},
		Args:    cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			return runListMetadataTypes(cmd.Context(), apiVersion, asJSON, refresh)
		},
	}
	cmd.Flags().BoolVar(&asJSON, "json", false, "output JSON")
	cmd.Flags().BoolVar(&refresh, "refresh", false, "ignore the cache and re-fetch from the org")
	cmd.Flags().StringVar(&apiVersion, "api-version", sfapi.DefaultAPIVersion, "Metadata API version")
	addTargetOrgFlag(cmd)
	return cmd
}

func runListMetadataTypes(ctx context.Context, apiVersion string, asJSON, refresh bool) error {
	org, err := auth.Resolve(targetOrg)
	if err != nil {
		return err
	}
	client := newMDClient(org)
	client.APIVersion = strings.TrimPrefix(apiVersion, "v")

	res, cached, err := client.DescribeMetadataCached(ctx, refresh)
	if err != nil {
		return err
	}
	if asJSON {
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		return enc.Encode(res)
	}
	return printMetadataTypes(res, cached)
}

func printMetadataTypes(res *mdapi.DescribeResult, cached bool) error {
	tw := tabwriter.NewWriter(os.Stdout, 0, 2, 2, ' ', 0)
	fmt.Fprintln(tw, "XML NAME\tCHILD XML NAMES\tDIRECTORY\tIN FOLDER\tMETA FILE\tSUFFIX")
	fmt.Fprintln(tw, "────────\t───────────────\t─────────\t─────────\t─────────\t──────")
	for _, o := range res.Objects {
		fmt.Fprintf(tw, "%s\t%s\t%s\t%t\t%t\t%s\n",
			o.Name, strings.Join(o.ChildXMLNames, ", "), o.DirectoryName, o.InFolder, o.MetaFile, o.Suffix)
	}
	if err := tw.Flush(); err != nil {
		return err
	}
	src := "live"
	if cached {
		src = "cached; --refresh to update"
	}
	fmt.Printf("\n%d type(s) (%s)\n", len(res.Objects), src)
	return nil
}

func printOrgs(orgs []*auth.OrgSummary) error {
	if len(orgs) == 0 {
		fmt.Println("No authenticated orgs found in ~/.sfdx.")
		return nil
	}
	tw := tabwriter.NewWriter(os.Stdout, 0, 2, 2, ' ', 0)
	fmt.Fprintln(tw, "\tALIAS\tUSERNAME\tORG ID\tTYPE")
	fmt.Fprintln(tw, "\t─────\t────────\t──────\t────")
	for _, o := range orgs {
		marker := ""
		if o.IsDefault {
			marker = "▸"
		}
		fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\n",
			marker, strings.Join(o.Aliases, ","), o.Username, o.OrgID, orgType(o))
	}
	if err := tw.Flush(); err != nil {
		return err
	}
	fmt.Printf("\n%d org(s); ▸ = default\n", len(orgs))
	return nil
}

// orgType renders a human label for the org's nature.
func orgType(o *auth.OrgSummary) string {
	t := "production"
	switch {
	case o.IsScratch:
		t = "scratch"
	case o.IsSandbox:
		t = "sandbox"
	}
	if o.IsDevHub {
		t += " (devhub)"
	}
	return t
}

func newOrgDisplayCmd() *cobra.Command {
	var refresh, showToken bool
	cmd := &cobra.Command{
		Use:   "display [target]",
		Short: "Show a stored org's credentials (reads sf's ~/.sfdx)",
		Long:  "Resolve an org from sf's stored credentials and print it. The target may be\ngiven as a positional argument, via -o, or omitted to use the default org.",
		Args:  cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			// Precedence: positional target > --target-org > configured default.
			target := targetOrg
			if len(args) == 1 {
				target = args[0]
			}
			return runOrgDisplay(cmd.Context(), target, refresh, showToken)
		},
	}
	cmd.Flags().BoolVar(&refresh, "refresh", false, "refresh the access token before displaying")
	cmd.Flags().BoolVar(&showToken, "show-token", false, "print the full access token (sensitive)")
	addTargetOrgFlag(cmd)
	return cmd
}

func runOrgDisplay(ctx context.Context, target string, refresh, showToken bool) error {
	org, err := auth.Resolve(target)
	if err != nil {
		return err
	}
	if refresh {
		if err := org.Refresh(ctx); err != nil {
			return err
		}
	}

	token := mask(org.AccessToken)
	if showToken {
		token = org.AccessToken
	}
	fmt.Printf("Username     %s\n", org.Username)
	fmt.Printf("Alias        %s\n", org.Alias)
	fmt.Printf("Org ID       %s\n", org.OrgID)
	fmt.Printf("Instance URL %s\n", org.InstanceURL)
	fmt.Printf("Login URL    %s\n", org.LoginURL)
	fmt.Printf("Sandbox      %t\n", org.IsSandbox)
	fmt.Printf("Access Token %s\n", token)
	return nil
}

// mask hides the middle of a secret, keeping the first 6 and last 4 chars.
func mask(s string) string {
	if len(s) <= 12 {
		return "****"
	}
	return s[:6] + "…" + s[len(s)-4:]
}

func newOrgOpenCmd() *cobra.Command {
	var path string
	var urlOnly bool
	cmd := &cobra.Command{
		Use:   "open [target]",
		Short: "Open an org in the default browser (like sf org open)",
		Long: "Open a logged-in browser session for an org using its stored credentials.\n" +
			"The target may be given as a positional argument, via -o, or omitted to use\n" +
			"the default org. Use --path to land on a specific page and --url-only to\n" +
			"print the login URL instead of opening a browser.",
		Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			// Precedence: positional target > --target-org > configured default.
			target := targetOrg
			if len(args) == 1 {
				target = args[0]
			}
			return runOrgOpen(cmd.Context(), target, path, urlOnly)
		},
	}
	cmd.Flags().StringVarP(&path, "path", "p", "", "relative path to open (e.g. lightning/setup/SetupOneHome/home)")
	cmd.Flags().BoolVarP(&urlOnly, "url-only", "r", false, "print the login URL instead of opening a browser")
	addTargetOrgFlag(cmd)
	return cmd
}

func runOrgOpen(ctx context.Context, target, path string, urlOnly bool) error {
	org, err := auth.Resolve(target)
	if err != nil {
		return err
	}
	// Refresh so the frontdoor session is minted from a valid access token.
	if err := org.Refresh(ctx); err != nil {
		return err
	}

	loginURL := frontDoorURL(org.InstanceURL, org.AccessToken, path)
	if urlOnly {
		fmt.Println(loginURL)
		return nil
	}
	if err := openBrowser(loginURL); err != nil {
		return fmt.Errorf("open browser: %w (try --url-only)", err)
	}
	fmt.Printf("Opening %s in your browser…\n", org.Username)
	return nil
}

// frontDoorURL builds a Salesforce frontdoor.jsp URL that logs the browser into
// the org using the access token, optionally redirecting to a relative path.
func frontDoorURL(instanceURL, accessToken, path string) string {
	base := strings.TrimRight(instanceURL, "/")
	q := url.Values{"sid": {accessToken}}
	if path != "" {
		q.Set("retURL", "/"+strings.TrimLeft(path, "/"))
	}
	return base + "/secur/frontdoor.jsp?" + q.Encode()
}

// openBrowser launches the OS default browser pointed at target.
func openBrowser(target string) error {
	var name string
	var args []string
	switch runtime.GOOS {
	case "darwin":
		name = "open"
	case "windows":
		name, args = "rundll32", []string{"url.dll,FileProtocolHandler"}
	default:
		name = "xdg-open"
	}
	args = append(args, target)
	return exec.Command(name, args...).Start()
}
