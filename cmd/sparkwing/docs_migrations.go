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
	var wf docsWebFlags
	fs.StringVarP(&output, "output", "o", "pretty", "pretty | table | json | plain")
	registerWebFlags(fs, &wf, false)
	if err := parseAndCheck(cmdDocsMigrationsList, fs, args); err != nil {
		if errors.Is(err, errHelpRequested) {
			return nil
		}
		return err
	}
	if fs.NArg() > 0 {
		return fmt.Errorf("docs migrations list: unexpected positional %q", fs.Arg(0))
	}
	ctx, cancel := newWebContext()
	defer cancel()
	if !wf.web {
		entries := docs.MigrationsList()
		if err := renderMigrationsList(entries, output); err != nil {
			return err
		}
		renderStaleCLIHint(entries, output)
		return nil
	}
	client := docs.NewWebClient()
	client.NoCache = wf.noCache
	entries, err := client.MigrationIndex(ctx)
	if err != nil {
		return fmt.Errorf("docs migrations list --web: %w", err)
	}
	return renderMigrationsList(entries, output)
}

func runDocsMigrationsRead(args []string) error {
	fs := flag.NewFlagSet(cmdDocsMigrationsRead.Path, flag.ContinueOnError)
	var output string
	var wf docsWebFlags
	fs.StringVarP(&output, "output", "o", "markdown", "markdown | plain")
	registerWebFlags(fs, &wf, true)
	if err := parseAndCheck(cmdDocsMigrationsRead, fs, args); err != nil {
		if errors.Is(err, errHelpRequested) {
			return nil
		}
		return err
	}
	if wf.version == "" && fs.NArg() > 0 {
		wf.version = fs.Arg(0)
	}
	if wf.version == "" {
		PrintHelp(cmdDocsMigrationsRead, os.Stderr)
		return errors.New("docs migrations read: --version is required (e.g. --version v0.4.0)")
	}
	if !semver.IsValid(wf.version) {
		return fmt.Errorf("docs migrations read: %q is not a valid semver (e.g. v0.4.0); run `sparkwing docs migrations list` to see available versions", wf.version)
	}

	ctx, cancel := newWebContext()
	defer cancel()

	var body string
	if wf.web {
		resolution, err := resolveSource(ctx, wf)
		if err != nil {
			return err
		}
		printDiscoveryWarning(resolution)
		b, ferr := fetchMigrationWeb(ctx, resolution)
		if ferr != nil {
			return ferr
		}
		body = b
	} else {
		b, err := docs.MigrationsRead(wf.version)
		if err != nil {
			var sb strings.Builder
			fmt.Fprintf(&sb, "%v\n\navailable versions in this binary:\n", err)
			for _, e := range docs.MigrationsList() {
				fmt.Fprintf(&sb, "  %s\n", e.Version)
			}
			sb.WriteString("\nRerun with --web to fetch from sparkwing.dev.")
			return errors.New(sb.String())
		}
		body = b
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
	var wf docsWebFlags
	fs.StringVarP(&output, "output", "o", "markdown", "markdown | plain")
	registerWebFlags(fs, &wf, false)
	if err := parseAndCheck(cmdDocsMigrationsBetween, fs, args); err != nil {
		if errors.Is(err, errHelpRequested) {
			return nil
		}
		return err
	}
	if fs.NArg() > 0 {
		return fmt.Errorf("docs migrations between: unexpected positional %q (use --from/--to)", fs.Arg(0))
	}
	if *from != "" && *to != "" && semver.Compare(*from, *to) > 0 {
		fmt.Fprintf(os.Stderr, "%s: --from (%s) is newer than --to (%s); did you swap the args?\n",
			color.Dim("warning"), *from, *to)
	}

	ctx, cancel := newWebContext()
	defer cancel()

	var entries []docs.MigrationEntry
	var bodyFetcher func(version string) (string, error)

	if wf.web {
		client := docs.NewWebClient()
		client.NoCache = wf.noCache
		all, err := client.MigrationIndex(ctx)
		if err != nil {
			return fmt.Errorf("docs migrations between --web: %w", err)
		}
		entries = filterAndOrderBetween(all, *from, *to)
		bodyFetcher = func(version string) (string, error) {
			return client.Migration(ctx, version)
		}
	} else {
		picked, err := docs.MigrationsBetween(*from, *to)
		if err != nil {
			return fmt.Errorf("docs migrations between: %w", err)
		}
		entries = picked
		bodyFetcher = docs.MigrationsRead
	}

	switch strings.ToLower(output) {
	case "markdown", "plain", "":
		body, err := renderBetweenMarkdown(*from, *to, entries, bodyFetcher)
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

// filterAndOrderBetween applies the same (from, to] filter as
// docs.MigrationsBetween, but to an externally-supplied index (e.g.
// the web-fetched MigrationIndex). The local-only variant lives in
// pkg/docs/migrations.go; this duplicates the logic so the web path
// doesn't have to round-trip through the embed.
func filterAndOrderBetween(all []docs.MigrationEntry, from, to string) []docs.MigrationEntry {
	if from == "" {
		from = "v0.0.0"
	}
	if to == "" {
		newest := ""
		for _, e := range all {
			if newest == "" || semver.Compare(e.Version, newest) > 0 {
				newest = e.Version
			}
		}
		to = newest
	}
	if !semver.IsValid(from) || !semver.IsValid(to) {
		return nil
	}
	var picked []docs.MigrationEntry
	for _, e := range all {
		if !semver.IsValid(e.Version) {
			continue
		}
		if semver.Compare(e.Version, from) > 0 && semver.Compare(e.Version, to) <= 0 {
			picked = append(picked, e)
		}
	}
	for i := 1; i < len(picked); i++ {
		for j := i; j > 0 && semver.Compare(picked[j-1].Version, picked[j].Version) > 0; j-- {
			picked[j-1], picked[j] = picked[j], picked[j-1]
		}
	}
	return picked
}

// renderBetweenMarkdown is the shared formatter for "between" output.
// Mirrors docs.MigrationsBetweenMarkdown but parameterizes the
// per-version body fetch so the web path can pull from sparkwing.dev
// without going through the embed.
func renderBetweenMarkdown(from, to string, entries []docs.MigrationEntry, fetch func(string) (string, error)) (string, error) {
	var b strings.Builder
	displayFrom := from
	if displayFrom == "" {
		displayFrom = "v0.0.0"
	}
	displayTo := to
	if displayTo == "" && len(entries) > 0 {
		displayTo = entries[len(entries)-1].Version
	}
	if displayTo == "" {
		displayTo = "(latest)"
	}
	fmt.Fprintf(&b, "# Migration: %s -> %s\n\n", displayFrom, displayTo)
	switch len(entries) {
	case 0:
		b.WriteString("(no migration guides apply in this range)\n")
		return b.String(), nil
	case 1:
		b.WriteString("(1 guide applies in this range)\n\n")
	default:
		fmt.Fprintf(&b, "(%d guides apply in this range)\n\n", len(entries))
	}
	for i, e := range entries {
		body, err := fetch(e.Version)
		if err != nil {
			return "", err
		}
		if i > 0 {
			b.WriteString("\n")
		}
		b.WriteString("---\n\n")
		b.WriteString(strings.TrimSpace(body))
		b.WriteString("\n")
	}
	return b.String(), nil
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
