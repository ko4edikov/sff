// Package cli wires sff's command tree using cobra.
package cli

import (
	"fmt"
	"os"

	"github.com/spf13/cobra"

	"github.com/ko4edikov/sff/pkg/auth"
	"github.com/ko4edikov/sff/pkg/mdapi"
)

// newMDClient builds a Metadata API client and, when SFF_DEBUG is set, wires a
// stderr logger so verbose SOAP tracing keeps working at the CLI layer. The
// mdapi package itself stays silent by default for library consumers.
func newMDClient(org *auth.Org) *mdapi.Client {
	c := mdapi.New(org)
	if os.Getenv("SFF_DEBUG") != "" {
		c.Logger = mdapi.NewWriterLogger(os.Stderr, "[sff:mdapi] ")
	}
	return c
}

// targetOrg holds the value of the --target-org/-o flag for whichever command
// is running. It's added only to commands that act on a single org (not, say,
// `org list`). Empty means "use the configured default org".
var targetOrg string

// addTargetOrgFlag registers the -o/--target-org flag on a command, bound to
// the shared targetOrg var.
func addTargetOrgFlag(cmd *cobra.Command) {
	cmd.Flags().StringVarP(&targetOrg, "target-org", "o", "",
		"target org (alias or username; default: the configured org)")
}

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
	root.AddCommand(newQueryCmd())
	root.AddCommand(newOrgCmd())
	root.AddCommand(newRetrieveCmd())
	root.AddCommand(newDeployCmd())
	root.AddCommand(newDiffCmd())
	return root
}

// Execute runs the sff command tree, returning any error to the caller.
func Execute(version string) error {
	return newRootCmd(version).Execute()
}

// ExitError signals a specific process exit code without an error message —
// used e.g. by `sff diff` to exit 1 when files differ (like `git diff`).
type ExitError struct{ Code int }

func (e *ExitError) Error() string { return fmt.Sprintf("exit %d", e.Code) }
