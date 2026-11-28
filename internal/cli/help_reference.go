package cli

import (
	"bytes"
	"fmt"
	"io"
	"sort"
	"strings"

	"github.com/cpuguy83/go-md2man/v2/md2man"
	"github.com/spf13/cobra"
)

// newHelpReferenceCmd builds `sff help reference`: the whole command tree rendered as a single
// flat document (like a man page), instead of nested per-command --help.
func newHelpReferenceCmd() *cobra.Command {
	var markdown, man bool
	cmd := &cobra.Command{
		Use:   "reference [command...]",
		Short: "Print help for all commands as one document",
		Long: "Print the help of every command (descriptions, usage, examples and flags)\n" +
			"as a single flat document instead of nested --help pages. Pass a command\n" +
			"path (e.g. `deploy`) to limit it to that command and its subcommands.\n" +
			"Pipe it to a pager: sff help reference | less. With --man it emits a roff man page\n" +
			"so you can install it and use `man sff`.",
		Example: "  sff help reference\n  sff help reference deploy\n  sff help reference --markdown > sff.md\n  sff help reference --man > /tmp/sff.1 && man /tmp/sff.1",
		RunE: func(cmd *cobra.Command, args []string) error {
			start := cmd.Root()
			if len(args) > 0 {
				c, _, err := start.Find(args)
				if err != nil || c == nil || c == start {
					return fmt.Errorf("unknown command %q", strings.Join(args, " "))
				}
				start = c
			}
			if man {
				var buf bytes.Buffer
				fmt.Fprintf(&buf, "# SFF 1 \"\" \"sff %s\" \"sff Manual\"\n\n", cmd.Root().Version)
				writeManual(&buf, start, true)
				_, err := cmd.OutOrStdout().Write(md2man.Render(buf.Bytes()))
				return err
			}
			writeManual(cmd.OutOrStdout(), start, markdown)
			return nil
		},
	}
	cmd.Flags().BoolVar(&markdown, "markdown", false, "render as Markdown")
	cmd.Flags().BoolVar(&man, "man", false, "render as a man page (roff); install with: sff help reference --man > $(brew --prefix)/share/man/man1/sff.1")
	return cmd
}

func writeManual(w io.Writer, c *cobra.Command, md bool) {
	writeManualEntry(w, c, md)
	subs := append([]*cobra.Command(nil), c.Commands()...)
	sort.Slice(subs, func(i, j int) bool { return subs[i].Name() < subs[j].Name() })
	for _, s := range subs {
		if s.Hidden || s.Name() == "help" || s.Name() == "completion" || s.Name() == "reference" {
			continue
		}
		writeManual(w, s, md)
	}
}

func writeManualEntry(w io.Writer, c *cobra.Command, md bool) {
	depth := strings.Count(c.CommandPath(), " ")
	title := c.CommandPath()
	if md {
		fmt.Fprintf(w, "%s %s\n\n", strings.Repeat("#", depth+1), title)
	} else {
		fmt.Fprintf(w, "%s\n%s\n", strings.Repeat("=", 72), title)
		fmt.Fprintf(w, "%s\n", strings.Repeat("=", 72))
	}
	desc := c.Long
	if desc == "" {
		desc = c.Short
	}
	if desc != "" {
		fmt.Fprintf(w, "%s\n\n", strings.TrimSpace(desc))
	}
	fmt.Fprintf(w, "%s\n", section("Usage", md))
	fmt.Fprintf(w, "%s\n\n", block(c.UseLine(), md))
	if c.Example != "" {
		fmt.Fprintf(w, "%s\n", section("Examples", md))
		fmt.Fprintf(w, "%s\n\n", block(strings.TrimRight(c.Example, "\n"), md))
	}
	if f := strings.TrimRight(c.LocalFlags().FlagUsages(), "\n"); f != "" {
		fmt.Fprintf(w, "%s\n", section("Flags", md))
		fmt.Fprintf(w, "%s\n\n", block(f, md))
	}
}

func section(name string, md bool) string {
	if md {
		return "**" + name + "**\n"
	}
	return name + ":"
}

func block(s string, md bool) string {
	if md {
		return "```\n" + s + "\n```"
	}
	return s
}

// newHelpCmd replaces cobra's built-in help command so that `help` can carry
// the `reference` subcommand while `sff help <command>` still behaves as usual.
func newHelpCmd() *cobra.Command {
	help := &cobra.Command{
		Use:   "help [command]",
		Short: "Help about any command",
		RunE: func(cmd *cobra.Command, args []string) error {
			c, _, err := cmd.Root().Find(args)
			if c == nil || err != nil {
				return fmt.Errorf("unknown help topic %q", strings.Join(args, " "))
			}
			return c.Help()
		},
	}
	help.AddCommand(newHelpReferenceCmd())
	return help
}
