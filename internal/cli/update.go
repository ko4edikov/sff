package cli

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/spf13/cobra"

	"github.com/ko4edikov/sff/pkg/selfupdate"
)

const releaseRepo = "ko4edikov/sff"

// newUpdateCmd builds `sff update`, which replaces the running binary with the
// latest (or a pinned) GitHub release.
func newUpdateCmd(buildVersion string) *cobra.Command {
	var (
		check   bool
		force   bool
		version string
	)
	cmd := &cobra.Command{
		Use:   "update",
		Short: "Update sff to the latest release",
		Long: "Download the latest sff release from GitHub, verify its checksum and replace\n" +
			"the running binary. Use --check to only report whether an update is available.",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			current := strings.Fields(buildVersion + " ")[0]
			u := selfupdate.New(releaseRepo)
			ctx := cmd.Context()

			tag := version
			if tag == "" {
				var err error
				if tag, err = u.LatestTag(ctx); err != nil {
					return err
				}
			}
			if !strings.HasPrefix(tag, "v") {
				tag = "v" + tag
			}

			cmp, comparable := selfupdate.Compare(current, tag)
			out := cmd.OutOrStdout()
			if comparable && cmp == 0 && !force {
				fmt.Fprintf(out, "sff is up to date (%s)\n", current)
				return nil
			}
			if comparable && cmp > 0 && version == "" && !force {
				fmt.Fprintf(out, "sff %s is newer than the latest release (%s)\n", current, tag)
				return nil
			}
			if check {
				fmt.Fprintf(out, "update available: %s → %s (run `sff update`)\n", current, tag)
				return nil
			}

			exe, err := os.Executable()
			if err != nil {
				return err
			}
			if exe, err = filepath.EvalSymlinks(exe); err != nil {
				return err
			}
			fmt.Fprintf(out, "Downloading sff %s…\n", tag)
			bin, err := u.Download(ctx, tag)
			if err != nil {
				return err
			}
			if err := selfupdate.Replace(exe, bin); err != nil {
				return err
			}
			fmt.Fprintf(out, "Updated sff %s → %s (%s)\n", current, tag, exe)
			return nil
		},
	}
	cmd.Flags().BoolVar(&check, "check", false, "only check whether a newer release exists")
	cmd.Flags().BoolVar(&force, "force", false, "reinstall even if already on that version")
	cmd.Flags().StringVar(&version, "version", "", "install a specific release tag (e.g. v0.8.0) instead of the latest")
	return cmd
}
