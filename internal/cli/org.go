package cli

import (
	"context"
	"fmt"

	"github.com/spf13/cobra"

	"github.com/ko4edikov/sff/internal/auth"
)

func newOrgCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "org",
		Short: "Inspect stored orgs",
	}
	cmd.AddCommand(newOrgDisplayCmd())
	return cmd
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
