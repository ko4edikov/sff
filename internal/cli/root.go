// Package cli wires sff's command tree using cobra.
package cli

import (
	"github.com/spf13/cobra"
)

// targetOrg is the shared --target-org/-o persistent flag, inherited by every
// subcommand. Empty means "use the configured default org".
var targetOrg string

// newRootCmd builds the root command and attaches all subcommands.
func newRootCmd(version string) *cobra.Command {
	root := &cobra.Command{
		Use:     "sff",
		Short:   "Fast native Salesforce CLI",
		Long:    "sff — a fast, native Salesforce CLI that reuses the credentials\nalready stored by the official sf CLI (no Node.js/oclif startup overhead).",
		Version: version,
		// Runtime errors are printed by Execute; don't dump usage for them.
		SilenceUsage:  true,
		SilenceErrors: true,
	}
	root.PersistentFlags().StringVarP(&targetOrg, "target-org", "o", "",
		"target org (alias or username; default: the configured org)")

	root.AddCommand(newQueryCmd())
	root.AddCommand(newOrgCmd())
	return root
}

// Execute runs the sff command tree, returning any error to the caller.
func Execute(version string) error {
	return newRootCmd(version).Execute()
}
