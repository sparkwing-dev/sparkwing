// `sparkwing docs migrations` exposes the per-version migration
// guides shipped under docs/migrations/ as a typed surface on top of
// the embedded docs corpus. The intent is agent-facing: one
// invocation should drop a full migration context blob into a chat
// without the agent needing to know which slugs exist or how to
// concatenate them.
package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strings"

	flag "github.com/spf13/pflag"
	"golang.org/x/mod/semver"

	"github.com/sparkwing-dev/sparkwing/pkg/color"
	"github.com/sparkwing-dev/sparkwing/pkg/docs"
)

func runDocsMigrations(args []string) error {
	if len(args) == 0 {
		PrintHelp(cmdDocsMigrations, os.Stderr)
		return errors.New("docs migrations: missing subcommand")
	}
	switch args[0] {
	case "list":
		return runDocsMigrationsList(args[1:])
	case "read":
		return runDocsMigrationsRead(args[1:])
	case "between":
		return runDocsMigrationsBetween(args[1:])
	case "help", "-h", "--help":
		PrintHelp(cmdDocsMigrations, os.Stdout)
		return nil
	default:
		PrintHelp(cmdDocsMigrations, os.Stderr)
		return fmt.Errorf("docs migrations: unknown verb %q (valid: list, read, between)", args[0])
	}
}

func runDocsMigrationsList(args []string) error {
	fs := flag.NewFlagSet(cmdDocsMigrationsList.Path, flag.ContinueOnError)
	var output string
	fs.StringVarP(&output, "output", "o", "pretty", "pretty | table | json | plain")
	if err := parseAndCheck(cmdDocsMigrationsList, fs, args); err != nil {
		if errors.Is(err, errHelpRequested) {
			return nil
		}
		return err
	}
	if fs.NArg() > 0 {
		return fmt.Errorf("docs migrations list: unexpected positional %q", fs.Arg(0))
	}
	entries := docs.MigrationsList()
	if err := renderMigrationsList(entries, output); err != nil {
		return err
	}
	renderStaleCLIHint(entries, output)
	return nil
}

func runDocsMigrationsRead(args []string) error {
	fs := flag.NewFlagSet(cmdDocsMigrationsRead.Path, flag.ContinueOnError)
	version := fs.String("version", "", "migration guide version (e.g. v0.4.0)")
	var output string
	fs.StringVarP(&output, "output", "o", "markdown", "markdown | plain")
	if err := parseAndCheck(cmdDocsMigrationsRead, fs, args); err != nil {
		if errors.Is(err, errHelpRequested) {
			return nil
		}
		return err
	}
	if *version == "" && fs.NArg() > 0 {
		*version = fs.Arg(0)
	}
	if *version == "" {
		PrintHelp(cmdDocsMigrationsRead, os.Stderr)
		return errors.New("docs migrations read: --version is required (e.g. --version v0.4.0)")
	}
	if !semver.IsValid(*version) {
		return fmt.Errorf("docs migrations read: %q is not a valid semver (e.g. v0.4.0); run `sparkwing docs migrations list` to see available versions", *version)
	}
	body, err := docs.MigrationsRead(*version)
	if err != nil {
		var b strings.Builder
		fmt.Fprintf(&b, "%v\n\navailable versions:\n", err)
		for _, e := range docs.MigrationsList() {
			fmt.Fprintf(&b, "  %s\n", e.Version)
		}
		return errors.New(strings.TrimRight(b.String(), "\n"))
	}
	switch strings.ToLower(output) {
	case "markdown", "plain", "":
		fmt.Print(body)
		if !strings.HasSuffix(body, "\n") {
			fmt.Println()
		}
	default:
		return fmt.Errorf("unknown output format %q (valid: markdown, plain)", output)
	}
	return nil
}

func runDocsMigrationsBetween(args []string) error {
	fs := flag.NewFlagSet(cmdDocsMigrationsBetween.Path, flag.ContinueOnError)
	from := fs.String("from", "", "exclusive lower bound (default v0.0.0 = every guide up through --to)")
	to := fs.String("to", "", "inclusive upper bound (default = highest version this CLI knows about)")
	var output string
	fs.StringVarP(&output, "output", "o", "markdown", "markdown | plain")
	if err := parseAndCheck(cmdDocsMigrationsBetween, fs, args); err != nil {
		if errors.Is(err, errHelpRequested) {
			return nil
		}
		return err
	}
	if fs.NArg() > 0 {
		return fmt.Errorf("docs migrations between: unexpected positional %q (use --from/--to)", fs.Arg(0))
	}
	entries, err := docs.MigrationsBetween(*from, *to)
	if err != nil {
		return fmt.Errorf("docs migrations between: %w", err)
	}
	displayFrom := *from
	if displayFrom == "" {
		displayFrom = "v0.0.0"
	}
	if *from != "" && *to != "" && semver.Compare(*from, *to) > 0 {
		fmt.Fprintf(os.Stderr, "%s: --from (%s) is newer than --to (%s); did you swap the args?\n",
			color.Dim("warning"), *from, *to)
	}
	switch strings.ToLower(output) {
	case "markdown", "plain", "":
		body, err := docs.MigrationsBetweenMarkdown(*from, *to, entries)
		if err != nil {
			return fmt.Errorf("docs migrations between: %w", err)
		}
		fmt.Print(body)
		if !strings.HasSuffix(body, "\n") {
			fmt.Println()
		}
	default:
		return fmt.Errorf("unknown output format %q (valid: markdown, plain)", output)
	}
	return nil
}

func renderMigrationsList(entries []docs.MigrationEntry, output string) error {
	switch strings.ToLower(output) {
	case "json":
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		return enc.Encode(entries)
	case "plain":
		for _, e := range entries {
			fmt.Println(e.Version)
		}
		return nil
	case "pretty", "table", "":
		if len(entries) == 0 {
			fmt.Println(color.Dim("(no migration guides in this binary)"))
			return nil
		}
		verW := len("VERSION")
		dateW := len("DATE")
		for _, e := range entries {
			if n := len(e.Version); n > verW {
				verW = n
			}
			if n := len(e.Date); n > dateW {
				dateW = n
			}
		}
		fmt.Printf("%s  %s  %s  %s\n",
			color.Bold(fmt.Sprintf("%-*s", verW, "VERSION")),
			color.Bold(fmt.Sprintf("%-*s", dateW, "DATE")),
			color.Bold(fmt.Sprintf("%7s", "BYTES")),
			color.Bold("SUMMARY"))
		for _, e := range entries {
			summary := e.Summary
			const summaryCap = 70
			if len(summary) > summaryCap {
				summary = summary[:summaryCap-1] + "…"
			}
			fmt.Printf("%-*s  %-*s  %7d  %s\n",
				verW, e.Version,
				dateW, e.Date,
				e.Bytes,
				color.Dim(summary))
		}
		return nil
	default:
		return fmt.Errorf("unknown output format %q (valid: pretty, json, plain)", output)
	}
}

// renderStaleCLIHint prints a one-line footer to stderr when the
// CLI's own version is older than the highest embedded guide -- which
// happens after a `git pull` of the repo without rebuilding the
// binary. Without a network call, we can't say "the latest release
// is X"; we can only flag a local skew between binary and embed.
// Silent when the CLI version is unknown or matches/exceeds the
// newest embedded guide.
func renderStaleCLIHint(entries []docs.MigrationEntry, output string) {
	if strings.ToLower(output) == "json" || strings.ToLower(output) == "plain" {
		return
	}
	if len(entries) == 0 {
		return
	}
	cliVersion := installedVersion()
	if !semver.IsValid(cliVersion) {
		return
	}
	newest := entries[0].Version
	if semver.Compare(cliVersion, newest) >= 0 {
		return
	}
	fmt.Fprintf(os.Stderr,
		"%s this CLI is at %s; the newest embedded migration guide is %s. "+
			"Install a newer CLI (`sparkwing update`) to ensure you see every guide that applies.\n",
		color.Dim("note:"), cliVersion, newest)
}
