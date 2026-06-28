// Command sff is a fast, native Salesforce CLI focused on the daily-driver
// commands (query, deploy, retrieve). It avoids the Node.js/oclif startup
// overhead of the official `sf` CLI by shipping as a single static binary.
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/ko4edikov/sff/internal/auth"
	"github.com/ko4edikov/sff/internal/sfapi"
)

// version is overridden at build time via -ldflags "-X main.version=...".
var version = "dev"

func main() {
	if err := run(os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, "sff:", err)
		os.Exit(1)
	}
}

func run(args []string) error {
	if len(args) == 0 {
		usage()
		return nil
	}

	switch args[0] {
	case "version", "--version", "-v":
		fmt.Println("sff", version)
		return nil
	case "help", "--help", "-h":
		usage()
		return nil
	case "org":
		return cmdOrg(args[1:])
	case "query":
		return cmdQuery(args[1:])
	default:
		return fmt.Errorf("unknown command %q (run `sff help`)", args[0])
	}
}

// cmdOrg dispatches `sff org <subcommand>`.
func cmdOrg(args []string) error {
	if len(args) == 0 {
		return fmt.Errorf("usage: sff org display [target] [--refresh]")
	}
	switch args[0] {
	case "display":
		return cmdOrgDisplay(args[1:])
	default:
		return fmt.Errorf("unknown org subcommand %q", args[0])
	}
}

// cmdOrgDisplay resolves an org from sf's stored credentials and prints it,
// proving that file lookup, Keychain decryption, and (optionally) token refresh
// all work. The access token is masked unless --show-token is given.
func cmdOrgDisplay(args []string) error {
	fs := flag.NewFlagSet("org display", flag.ContinueOnError)
	refresh := fs.Bool("refresh", false, "refresh the access token before displaying")
	showToken := fs.Bool("show-token", false, "print the full access token (sensitive)")
	if err := fs.Parse(args); err != nil {
		return err
	}

	org, err := auth.Resolve(fs.Arg(0))
	if err != nil {
		return err
	}
	if *refresh {
		if err := org.Refresh(context.Background()); err != nil {
			return err
		}
	}

	token := mask(org.AccessToken)
	if *showToken {
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

// cmdQuery runs a SOQL query against an org and prints the results as a table
// (or raw JSON with --json).
func cmdQuery(args []string) error {
	fs := flag.NewFlagSet("query", flag.ContinueOnError)
	target := fs.String("o", "", "target org (alias or username; default: configured org)")
	fs.StringVar(target, "target-org", "", "alias for -o")
	asJSON := fs.Bool("json", false, "print raw JSON records instead of a table")

	// Allow flags either before or after the SOQL by interleaving flag and
	// positional parsing (standard library flag stops at the first positional).
	var positionals []string
	rest := args
	for len(rest) > 0 {
		if err := fs.Parse(rest); err != nil {
			return err
		}
		if fs.NArg() == 0 {
			break
		}
		positionals = append(positionals, fs.Arg(0))
		rest = fs.Args()[1:]
	}
	soql := strings.TrimSpace(strings.Join(positionals, " "))
	if soql == "" {
		return fmt.Errorf(`usage: sff query "SELECT Id, Name FROM Account LIMIT 10" [-o org] [--json]`)
	}

	org, err := auth.Resolve(*target)
	if err != nil {
		return err
	}
	start := time.Now()
	records, total, err := sfapi.New(org).Query(context.Background(), soql)
	elapsed := time.Since(start)
	if err != nil {
		return err
	}

	if *asJSON {
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		if err := enc.Encode(records); err != nil {
			return err
		}
		fmt.Fprintf(os.Stderr, "queried in %s\n", fmtDuration(elapsed))
		return nil
	}
	return printTable(records, total, elapsed)
}

// fmtDuration renders a duration with millisecond precision.
func fmtDuration(d time.Duration) string {
	return d.Round(time.Millisecond).String()
}

// printTable renders records as a column-aligned table, preserving SELECT order.
func printTable(records []json.RawMessage, total int, elapsed time.Duration) error {
	if len(records) == 0 {
		// A COUNT() query returns no records, only totalSize.
		if total > 0 {
			fmt.Printf("count: %d\n", total)
		} else {
			fmt.Println("No records found.")
		}
		fmt.Printf("(%s)\n", fmtDuration(elapsed))
		return nil
	}
	cols, err := sfapi.Columns(records[0])
	if err != nil {
		return err
	}

	tw := tabwriter.NewWriter(os.Stdout, 0, 2, 2, ' ', 0)
	fmt.Fprintln(tw, strings.Join(cols, "\t"))
	seps := make([]string, len(cols))
	for i, c := range cols {
		seps[i] = strings.Repeat("─", len([]rune(c)))
	}
	fmt.Fprintln(tw, strings.Join(seps, "\t"))

	for _, rec := range records {
		vals := make([]string, len(cols))
		for i, c := range cols {
			vals[i] = sfapi.Field(rec, c)
		}
		fmt.Fprintln(tw, strings.Join(vals, "\t"))
	}
	if err := tw.Flush(); err != nil {
		return err
	}
	fmt.Printf("\n%d row(s) returned (totalSize %d) in %s\n", len(records), total, fmtDuration(elapsed))
	return nil
}

// mask hides the middle of a secret, keeping the first and last 4 chars.
func mask(s string) string {
	if len(s) <= 12 {
		return "****"
	}
	return s[:6] + "…" + s[len(s)-4:]
}

func usage() {
	fmt.Print(`sff — fast Salesforce CLI

Usage:
  sff <command> [flags]

Commands:
  query "SELECT ..."     Run a SOQL query (flags: -o org, --json)
  org display [target]   Show a stored org's credentials (reads sf's ~/.sfdx)
  version                Print the sff version
  help                   Show this help

`)
}
