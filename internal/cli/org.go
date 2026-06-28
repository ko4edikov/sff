package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"text/tabwriter"

	"github.com/spf13/cobra"

	"github.com/ko4edikov/sff/internal/auth"
)

func newOrgCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "org",
		Short: "Inspect stored orgs",
	}
	cmd.AddCommand(newOrgDisplayCmd())
	cmd.AddCommand(newOrgListCmd())
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
	return cmd
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
