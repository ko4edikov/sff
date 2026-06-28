// Command sff is a fast, native Salesforce CLI focused on the daily-driver
// commands (query, deploy, retrieve). It avoids the Node.js/oclif startup
// overhead of the official `sf` CLI by shipping as a single static binary.
package main

import (
	"fmt"
	"os"

	"github.com/ko4edikov/sff/internal/cli"
)

// version is overridden at build time via -ldflags "-X main.version=...".
var version = "dev"

func main() {
	if err := cli.Execute(version); err != nil {
		fmt.Fprintln(os.Stderr, "sff:", err)
		os.Exit(1)
	}
}
