// Command sff is a fast, native Salesforce CLI focused on the daily-driver
// commands (query, deploy, retrieve). It avoids the Node.js/oclif startup
// overhead of the official `sf` CLI by shipping as a single static binary.
package main

import (
	"errors"
	"fmt"
	"os"

	"github.com/ko4edikov/sff/internal/cli"
)

// version, commit, and date are overridden at build time via -ldflags
// "-X main.version=... -X main.commit=... -X main.date=..." (see the Makefile).
var (
	version = "dev"
	commit  = "none"
	date    = "unknown"
)

// buildVersion renders the string shown by `sff --version`, folding in the build
// commit and date when they were injected at link time.
func buildVersion() string {
	if commit == "none" {
		return version
	}
	return fmt.Sprintf("%s (commit %s, built %s)", version, commit, date)
}

func main() {
	if err := cli.Execute(buildVersion()); err != nil {
		// A bare exit-code request (e.g. `sff diff` files-differ) exits quietly.
		if exit, ok := errors.AsType[*cli.ExitError](err); ok {
			os.Exit(exit.Code)
		}
		fmt.Fprintln(os.Stderr, "sff:", err)
		os.Exit(1)
	}
}
