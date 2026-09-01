package main

import (
	"errors"
	"fmt"
	"os"
	"strings"

	"github.com/sparkwing-dev/sparkwing/internal/ndjson"

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
	case "guides":
		return runDocsGuides(args[1:])
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

func runDocsGuides(args []string) error {
	fs := flag.NewFlagSet(cmdDocsGuides.Path, flag.ContinueOnError)
	var output string
	fs.StringVarP(&output, "output", "o", "pretty", "pretty | json | plain")
	if err := parseAndCheck(cmdDocsGuides, fs, args); err != nil {
		if errors.Is(err, errHelpRequested) {
			return nil
		}
		return err
	}
	list := docs.Guides()
	switch strings.ToLower(output) {
	case "json":

		return ndjson.Write(os.Stdout, list)
	case "plain":
		for _, g := range list {
			fmt.Println(g.Name)
		}
		return nil
	case "pretty", "":
		for _, g := range list {
			fmt.Printf("%s\n", color.Bold(g.Name))
			fmt.Printf("  %s\n", g.Summary)
			fmt.Printf("  %s %s\n", color.Dim("topics:"), strings.Join(g.Topics, ", "))
			fmt.Printf("  %s\n\n", color.Cyan("sparkwing docs read --guide "+g.Name))
		}
		return nil
	default:
		return fmt.Errorf("unknown output format %q (valid: pretty, json, plain)", output)
	}
}

func runDocsRead(args []string) error {
	fs := flag.NewFlagSet(cmdDocsRead.Path, flag.ContinueOnError)
	topic := fs.String("topic", "", "doc slug (e.g. getting-started, pipelines, mcp)")
	guide := fs.String("guide", "", "read a named set of topics instead of one (see `sparkwing docs guides`)")
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
	if *guide != "" {
		if *topic != "" {
			return errors.New("docs read: --topic and --guide are mutually exclusive")
		}
		body, err := docs.ReadGuide(*guide)
		if err != nil {
			return err
		}
		fmt.Print(body)
		return nil
	}
	if *topic == "" {
		PrintHelp(cmdDocsRead, os.Stderr)
		return errors.New("docs read: --topic is required (e.g. --topic getting-started), or --guide for a task-sized set")
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
	var topicsOnly, withBody bool
	fs.StringVarP(&query, "query", "q", "", "search terms (every token must match somewhere)")
	fs.StringVarP(&output, "output", "o", "pretty", "pretty | json | plain")
	fs.BoolVar(&withBody, "body", false, "print each matching section in full instead of a snippet")
	fs.BoolVar(&topicsOnly, "topics", false, "list matching topics instead of sections")
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
		return errors.New("docs search: --query is required (e.g. --query \"pull_request\")")
	}
	if topicsOnly {
		return renderDocsList(docs.Search(query), output)
	}
	if strings.EqualFold(output, "pretty") || output == "" {
		printExampleHits(searchExamples(query), 4)
	}
	return renderDocsSections(docs.SearchSections(query), query, withBody, output)
}

func renderDocsSections(hits []docs.Section, query string, withBody bool, output string) error {
	switch strings.ToLower(output) {
	case "json":

		return ndjson.Write(os.Stdout, hits)
	case "plain":
		for _, h := range hits {
			fmt.Printf("%s:%d\t%s\n", h.Slug, h.StartLine, sectionLabel(h))
		}
		return nil
	case "pretty", "":
		if len(hits) == 0 {
			fmt.Printf("no section matches %q\n", query)
			fmt.Printf("%s sparkwing docs search --query %q --topics\n",
				color.Dim("whole topics:"), query)
			return nil
		}
		for _, h := range hits {
			where := h.Slug
			if label := sectionLabel(h); label != "" {
				where += "  " + color.Bold(label)
			}
			fmt.Printf("%s  %s\n", where, color.Dim(fmt.Sprintf("(lines %d-%d)", h.StartLine, h.EndLine)))
			if withBody {
				fmt.Printf("%s\n\n", h.Body)
				continue
			}
			fmt.Printf("  %s\n\n", color.Dim(sectionSnippet(h, query)))
		}
		if !withBody {
			fmt.Printf("%s %s\n", color.Dim("read them in full:"),
				color.Cyan(fmt.Sprintf("sparkwing docs search -q %q --body", query)))
		}
		return nil
	default:
		return fmt.Errorf("unknown output format %q (valid: pretty, json, plain)", output)
	}
}

func sectionSnippet(s docs.Section, query string) string {
	tokens := strings.Fields(strings.ToLower(query))
	lines := strings.Split(s.Body, "\n")
	for _, line := range lines {
		low := strings.ToLower(line)
		if strings.HasPrefix(strings.TrimSpace(line), "#") {
			continue
		}
		for _, tok := range tokens {
			if strings.Contains(low, tok) {
				return truncateLine(strings.TrimSpace(line))
			}
		}
	}
	for _, line := range lines {
		if t := strings.TrimSpace(line); t != "" && !strings.HasPrefix(t, "#") {
			return truncateLine(t)
		}
	}
	return ""
}

func truncateLine(s string) string {
	const max = 110
	if len(s) <= max {
		return s
	}
	return s[:max] + "..."
}

func renderDocsList(entries []docs.Entry, output string) error {
	switch strings.ToLower(output) {
	case "json":

		return ndjson.Write(os.Stdout, entries)
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

func sectionLabel(h docs.Section) string {
	if h.Breadcrumb == "" {
		return h.Heading
	}
	if h.Heading == "" {
		return h.Breadcrumb
	}
	return h.Breadcrumb + " > " + h.Heading
}
