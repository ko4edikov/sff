package cli

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/spf13/cobra"

	"github.com/ko4edikov/sff/pkg/mdapi"
	"github.com/ko4edikov/sff/pkg/project"
	"github.com/ko4edikov/sff/pkg/sfapi"
)

func newManifestCmd() *cobra.Command {
	var projectDir, outDir, apiVersion, commit string
	var changed, asJSON bool
	cmd := &cobra.Command{
		Use:   "manifest",
		Short: "Generate package.xml and destructiveChanges.xml from git changes",
		Long: "List what changed in the git working tree (vs HEAD: staged, unstaged and\n" +
			"untracked files) with --changed, or what one commit introduced with --commit,\n" +
			"as a package.xml (components to deploy) and a destructiveChanges.xml\n" +
			"(components whose files were all deleted or renamed away). Works offline —\n" +
			"no org is needed — so it starts instantly. Output goes to stdout, to --out as\n" +
			"two files, or as JSON with --json. This is the same selection\n" +
			"sff deploy --changed/--commit deploys.",
		Example: `  sff manifest --changed
  sff manifest --commit HEAD --out manifest
  sff manifest --changed --json`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			if !changed && commit == "" {
				return fmt.Errorf("specify the change source with --changed or --commit")
			}
			version := strings.TrimPrefix(apiVersion, "v")
			if version == "" {
				start := projectDir
				if start == "" {
					start = "."
				}
				if proj, err := project.Find(start); err == nil && proj.SourceAPIVersion != "" {
					version = strings.TrimPrefix(proj.SourceAPIVersion, "v")
				} else {
					version = strings.TrimPrefix(sfapi.DefaultAPIVersion, "v")
				}
			}
			sel := deploySelection{projectDir: projectDir, changed: changed, commit: commit}
			cs, _, err := resolveGitChanges(sel, version, nil)
			if err != nil {
				return err
			}
			return writeManifests(cs.Deploy, cs.Destroy, outDir, asJSON)
		},
	}
	cmd.Flags().BoolVar(&changed, "changed", false, "git working tree vs HEAD (staged, unstaged, untracked)")
	cmd.Flags().StringVar(&commit, "commit", "", "the changes a git commit introduced (vs its first parent)")
	cmd.Flags().StringVar(&outDir, "out", "", "write package.xml and destructiveChanges.xml into this directory instead of stdout")
	cmd.Flags().BoolVar(&asJSON, "json", false, "print {package, destructive} as JSON ({Type: [members]})")
	cmd.Flags().StringVar(&projectDir, "project-dir", "", "sfdx project to resolve from (default: search up from cwd)")
	cmd.Flags().StringVar(&apiVersion, "api-version", "", "manifest API version (default: the project's sourceApiVersion)")
	cmd.MarkFlagsMutuallyExclusive("changed", "commit")
	cmd.MarkFlagsMutuallyExclusive("json", "out")
	return cmd
}

// writeManifests emits the deploy and destructive manifests as JSON, files in
// outDir, or XML on stdout. destructiveChanges.xml is only produced when there
// is something to delete.
func writeManifests(deploy, destroy *mdapi.Package, outDir string, asJSON bool) error {
	if asJSON {
		toMap := func(p *mdapi.Package) map[string][]string {
			m := map[string][]string{}
			for _, t := range p.Types {
				m[t.Name] = t.Members
			}
			return m
		}
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		return enc.Encode(map[string]map[string][]string{"package": toMap(deploy), "destructive": toMap(destroy)})
	}

	pkgXML, err := deploy.XML()
	if err != nil {
		return err
	}
	var destroyXML []byte
	if componentCount(destroy) > 0 {
		if destroyXML, err = destroy.XML(); err != nil {
			return err
		}
	}
	if outDir == "" {
		fmt.Printf("package.xml:\n%s", pkgXML)
		if destroyXML != nil {
			fmt.Printf("\ndestructiveChanges.xml:\n%s", destroyXML)
		}
		return nil
	}
	if err := os.MkdirAll(outDir, 0o755); err != nil {
		return err
	}
	write := func(name string, data []byte) error {
		p := filepath.Join(outDir, name)
		if err := os.WriteFile(p, data, 0o644); err != nil {
			return err
		}
		fmt.Println("wrote", p)
		return nil
	}
	if err := write("package.xml", pkgXML); err != nil {
		return err
	}
	if destroyXML != nil {
		return write("destructiveChanges.xml", destroyXML)
	}
	return nil
}
