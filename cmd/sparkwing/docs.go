// `sparkwing docs` is the agent + human entrypoint to the
// embedded sparkwing documentation. The /docs/ tree is shipped in
// the binary via pkg/docs (//go:embed), so an agent can answer
// "how does X work" without leaving the CLI: the docs always match
// the binary it's running, and there's no network roundtrip.
package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strings"

	flag "github.com/spf13/pflag"

	"github.com/sparkwing-dev/sparkwing/pkg/color"
	"github.com/sparkwing-dev/sparkwing/pkg/docs"
)

func runDocs(args []string) error {
	if len(args) == 0 {
		PrintHelp(cmdDocs, os.Stderr)
		return errors.New("docs: missing subcommand")
	}
	switch args[0] {
	case "list":
		return runDocsList(args[1:])
	case "read":
		return runDocsRead(args[1:])
	case "all":
		return runDocsAll(args[1:])
	case "search":
		return runDocsSearch(args[1:])
	case "migrations":
		return runDocsMigrations(args[1:])
	case "versions":
		return runDocsVersions(args[1:])
	case "cache":
		return runDocsCache(args[1:])
	case "help", "-h", "--help":
		PrintHelp(cmdDocs, os.Stdout)
		return nil
	default:
		PrintHelp(cmdDocs, os.Stderr)
		return fmt.Errorf("docs: unknown verb %q (valid: list, read, all, search, migrations, versions, cache)", args[0])
	}
}

func runDocsList(args []string) error {
	fs := flag.NewFlagSet(cmdDocsList.Path, flag.ContinueOnError)
	var output string
	var wf docsWebFlags
	fs.StringVarP(&output, "output", "o", "pretty", "pretty | json | plain")
	registerWebFlags(fs, &wf, true)
	if err := parseAndCheck(cmdDocsList, fs, args); err != nil {
		if errors.Is(err, errHelpRequested) {
			return nil
		}
		return err
	}
	ctx, cancel := newWebContext()
	defer cancel()
	resolution, err := resolveSource(ctx, wf)
	if err != nil {
		return err
	}
	printDiscoveryWarning(resolution)
	if !resolution.useWeb {
		return renderDocsList(docs.List(), output)
	}
	entries, err := resolution.client.DocIndex(ctx, resolution.version)
	if err != nil {
		return fmt.Errorf("docs list --web %s: %w", resolution.version, err)
	}
	return renderDocsList(entries, output)
}

func runDocsRead(args []string) error {
	fs := flag.NewFlagSet(cmdDocsRead.Path, flag.ContinueOnError)
	topic := fs.String("topic", "", "doc slug (e.g. getting-started, pipelines, mcp)")
	var wf docsWebFlags
	registerWebFlags(fs, &wf, true)
	if err := parseAndCheck(cmdDocsRead, fs, args); err != nil {
		if errors.Is(err, errHelpRequested) {
			return nil
		}
		return err
	}
	if *topic == "" && fs.NArg() > 0 {
		*topic = fs.Arg(0)
	}
	if *topic == "" {
		PrintHelp(cmdDocsRead, os.Stderr)
		return errors.New("docs read: --topic is required (e.g. --topic getting-started)")
	}
	ctx, cancel := newWebContext()
	defer cancel()
	resolution, err := resolveSource(ctx, wf)
	if err != nil {
		return err
	}
	printDiscoveryWarning(resolution)
	if !resolution.useWeb {
		body, err := docs.Read(*topic)
		if err != nil {
			var b strings.Builder
			fmt.Fprintf(&b, "%v\n\navailable topics:\n", err)
			for _, e := range docs.List() {
				fmt.Fprintf(&b, "  %s\n", e.Slug)
			}
			return errors.New(strings.TrimRight(b.String(), "\n"))
		}
		fmt.Print(body)
		if !strings.HasSuffix(body, "\n") {
			fmt.Println()
		}
		return nil
	}
	body, err := fetchDocWeb(ctx, resolution, *topic)
	if err != nil {
		return err
	}
	fmt.Print(body)
	if !strings.HasSuffix(body, "\n") {
		fmt.Println()
	}
	return nil
}

func runDocsAll(args []string) error {
	fs := flag.NewFlagSet(cmdDocsAll.Path, flag.ContinueOnError)
	if err := parseAndCheck(cmdDocsAll, fs, args); err != nil {
		if errors.Is(err, errHelpRequested) {
			return nil
		}
		return err
	}
	if fs.NArg() > 0 {
		return fmt.Errorf("docs all: unexpected positional %q", fs.Arg(0))
	}
	fmt.Print(docs.All())
	return nil
}

func runDocsSearch(args []string) error {
	fs := flag.NewFlagSet(cmdDocsSearch.Path, flag.ContinueOnError)
	var query string
	var output string
	fs.StringVarP(&query, "query", "q", "", "search terms (every token must match somewhere)")
	fs.StringVarP(&output, "output", "o", "pretty", "pretty | json | plain")
	if err := parseAndCheck(cmdDocsSearch, fs, args); err != nil {
		if errors.Is(err, errHelpRequested) {
			return nil
		}
		return err
	}
	if query == "" && fs.NArg() > 0 {
		query = strings.Join(fs.Args(), " ")
	}
	if query == "" {
		PrintHelp(cmdDocsSearch, os.Stderr)
		return errors.New("docs search: --query is required (e.g. --query \"warm pool\")")
	}
	hits := docs.Search(query)
	return renderDocsList(hits, output)
}

func renderDocsList(entries []docs.Entry, output string) error {
	switch strings.ToLower(output) {
	case "json":
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		return enc.Encode(entries)
	case "plain":
		for _, e := range entries {
			fmt.Println(e.Slug)
		}
		return nil
	case "pretty", "table", "":
		if len(entries) == 0 {
			fmt.Println(color.Dim("(no docs match)"))
			return nil
		}
		slugW := len("SLUG")
		titleW := len("TITLE")
		for _, e := range entries {
			if n := len(e.Slug); n > slugW {
				slugW = n
			}
			if n := len(e.Title); n > titleW {
				titleW = n
			}
		}
		const titleCap = 40
		titleW = min(titleW, titleCap)
		fmt.Printf("%s  %s  %s\n",
			color.Bold(fmt.Sprintf("%-*s", slugW, "SLUG")),
			color.Bold(fmt.Sprintf("%-*s", titleW, "TITLE")),
			color.Bold("SUMMARY"))
		for _, e := range entries {
			title := e.Title
			if len(title) > titleW {
				title = title[:titleW-1] + "…"
			}
			summary := e.Summary
			const summaryCap = 70
			if len(summary) > summaryCap {
				summary = summary[:summaryCap-1] + "…"
			}
			fmt.Printf("%-*s  %-*s  %s\n", slugW, e.Slug, titleW, title, color.Dim(summary))
		}
		return nil
	default:
		return fmt.Errorf("unknown output format %q (valid: pretty, json, plain)", output)
	}
}
