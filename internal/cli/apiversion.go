package cli

import (
	"context"
	"strings"

	"github.com/ko4edikov/sff/pkg/auth"
	"github.com/ko4edikov/sff/pkg/project"
	"github.com/ko4edikov/sff/pkg/sfapi"
)

const apiVersionUsage = "Metadata API version (default: sourceApiVersion from sfdx-project.json, else the org's latest)"

// resolveAPIVersion picks the API version: the --api-version flag, else the
// project's sourceApiVersion (searched up from projectDir, or cwd), else the
// highest version the org supports, else sfapi.DefaultAPIVersion. Commands that
// have no project pass projectDir "-" to skip the project lookup.
func resolveAPIVersion(ctx context.Context, flag, projectDir string, org *auth.Org) string {
	if flag != "" {
		return flag
	}
	if projectDir != "-" {
		if projectDir == "" {
			projectDir = "."
		}
		if proj, err := project.Find(projectDir); err == nil && proj.SourceAPIVersion != "" {
			return "v" + strings.TrimPrefix(proj.SourceAPIVersion, "v")
		}
	}
	if v, err := sfapi.LatestAPIVersion(ctx, org); err == nil {
		return v
	}
	return sfapi.DefaultAPIVersion
}
