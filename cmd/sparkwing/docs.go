package main

import (
	"encoding/json"
	"fmt"
	"os"
	"sort"
	"strconv"
	"strings"

	"github.com/sparkwing-dev/sparkwing/internal/ndjson"

	"github.com/sparkwing-dev/sparkwing/internal/clisurface"
	"github.com/sparkwing-dev/sparkwing/pkg/color"
	"github.com/sparkwing-dev/sparkwing/pkg/docs"
)

func runDocsList(inv clisurface.Invocation) error {
	output := inv.Output(color.IsInteractiveStdout())
	wf := docsWebFlags{web: inv.Bool("web"), version: inv.Value("version"), noCache: inv.Bool("no-cache")}
	query := inv.Value("search")
	paging := discoveryPaging{limit: 40, cursor: inv.Value("cursor")}
	if inv.Has("limit") {
		paging.limit, _ = strconv.Atoi(inv.Value("limit"))
	}
	guides, versions := inv.Bool("guides"), inv.Bool("versions")
	if guides && versions {
		return &clisurface.UsageError{Path: inv.Command.Path, Message: "--guides and --versions select separate listings"}
	}
	if guides || versions {
		mode := "--guides"
		allowed := map[string]bool{"output": true, "guides": true}
		if versions {
			mode = "--versions"
			allowed = map[string]bool{"output": true, "versions": true, "web": true, "no-cache": true}
		}
		var stray []string
		for _, f := range inv.Command.Flags {
			if inv.Has(f.Name) && !allowed[f.Name] {
				stray = append(stray, "--"+f.Name)
			}
		}
		if len(stray) > 0 {
			return &clisurface.UsageError{Path: inv.Command.Path, Message: fmt.Sprintf("%s: %s does not apply to this listing", mode, strings.Join(stray, ", "))}
		}
		if guides {
			return renderDocsGuides(output)
		}
		return renderDocsVersions(wf, output)
	}
	if paging.limit < 1 {
		return fmt.Errorf("--limit must be at least 1")
	}
	ctx, cancel := newWebContext()
	defer cancel()
	resolution, err := resolveSource(ctx, wf)
	if err != nil {
		return err
	}
	printDiscoveryWarning(resolution)
	if !resolution.useWeb {
		return renderDocsPage(docs.List(), query, paging, output)
	}
	entries, err := resolution.client.DocIndex(ctx, resolution.version)
	if err != nil {
		return fmt.Errorf("docs list --web %s: %w", resolution.version, err)
	}
	return renderDocsPage(entries, query, paging, output)
}

func renderDocsGuides(output string) error {
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

func runDocsRead(inv clisurface.Invocation) error {
	output := inv.Output(color.IsInteractiveStdout())
	topic, guide := inv.Value("topic"), inv.Value("guide")
	section, _ := strconv.Atoi(inv.Value("section"))
	wf := docsWebFlags{web: inv.Bool("web"), version: inv.Value("version"), noCache: inv.Bool("no-cache")}
	if inv.Bool("full") {
		var stray []string
		for _, f := range inv.Command.Flags {
			if inv.Has(f.Name) && f.Name != "full" && f.Name != "output" {
				stray = append(stray, "--"+f.Name)
			}
		}
		if len(stray) > 0 {
			return &clisurface.UsageError{Path: inv.Command.Path, Message: fmt.Sprintf("--full: %s does not apply to the whole embedded corpus", strings.Join(stray, ", "))}
		}
		return readAllDocs(output)
	}
	if inv.Has("section") && (section < 1 || guide != "" || wf.web) {
		return &clisurface.UsageError{Path: inv.Command.Path, Message: "--section requires a positive start_line, an embedded --topic, and no --guide or --web"}
	}
	if guide != "" {
		if wf.web || wf.version != "" || wf.noCache {
			return &clisurface.UsageError{Path: inv.Command.Path, Message: "--guide reads embedded docs; use --topic with --web, --version, or --no-cache"}
		}
		if topic != "" {
			return &clisurface.UsageError{Path: inv.Command.Path, Message: "--topic and --guide are mutually exclusive"}
		}
		body, err := docs.ReadGuide(guide)
		if err != nil {
			return err
		}
		return writeDocument(os.Stdout, topic, body, output)
	}
	if topic == "" {
		return &clisurface.UsageError{Path: inv.Command.Path, Message: "--topic or --guide is required"}
	}
	ctx, cancel := newWebContext()
	defer cancel()
	resolution, err := resolveSource(ctx, wf)
	if err != nil {
		return err
	}
	printDiscoveryWarning(resolution)
	if !resolution.useWeb {
		body, err := docs.Read(topic)
		if err != nil {
			return fmt.Errorf("%w; use sparkwing docs list --search <topic>", err)
		}
		if section > 0 {
			sections, err := docs.Sections(topic)
			if err != nil {
				return err
			}
			found := false
			for _, selected := range sections {
				if selected.StartLine == section {
					body, found = selected.Body, true
					break
				}
			}
			if !found {
				return fmt.Errorf("docs read: no section starts at line %d in %s; repeat docs search for this build", section, topic)
			}
		}
		if !strings.HasSuffix(body, "\n") {
			body += "\n"
		}
		return writeDocument(os.Stdout, topic, body, output)
	}
	body, err := fetchDocWeb(ctx, resolution, topic)
	if err != nil {
		return err
	}
	if !strings.HasSuffix(body, "\n") {
		body += "\n"
	}
	return writeDocument(os.Stdout, topic, body, output)
}

func readAllDocs(output string) error {
	if output == "json" {
		for _, entry := range docs.List() {
			body, err := docs.Read(entry.Slug)
			if err != nil {
				return err
			}
			if err := writeDocument(os.Stdout, entry.Slug, body, output); err != nil {
				return err
			}
		}
		return nil
	}
	return writeText(os.Stdout, "document", docs.All(), output)
}

func runDocsSearch(inv clisurface.Invocation) error {
	query := inv.Value("search")
	output := inv.Output(color.IsInteractiveStdout())
	topicsOnly, withBody := inv.Bool("topics"), inv.Bool("body")
	paging := discoveryPaging{limit: 20, cursor: inv.Value("cursor")}
	if inv.Has("limit") {
		paging.limit, _ = strconv.Atoi(inv.Value("limit"))
	}
	if paging.limit < 1 {
		return fmt.Errorf("--limit must be at least 1")
	}
	if query == "" {
		return &clisurface.UsageError{Path: inv.Command.Path, Message: "--search is required"}
	}
	if topicsOnly {
		return renderDocsPage(docs.Search(query), "", paging, output)
	}
	hits := docs.SearchSections(query)
	keys := make([]string, len(hits))
	for i, hit := range hits {
		keys[i] = fmt.Sprintf("%s:%d", hit.Slug, hit.StartLine)
	}
	start, end, page, err := paging.bounds(keys)
	if err != nil {
		return err
	}
	if output == "pretty" && paging.cursor == "" {
		printExampleHits(searchExamples(query), 4)
	}
	if err := renderDocsSections(hits[start:end], query, withBody, output); err != nil {
		return err
	}
	return page.write(output)
}

type sectionResult struct {
	Slug       string `json:"slug"`
	Heading    string `json:"heading"`
	Level      int    `json:"level"`
	StartLine  int    `json:"start_line"`
	EndLine    int    `json:"end_line"`
	Breadcrumb string `json:"breadcrumb,omitempty"`
	Snippet    string `json:"snippet"`
	Body       string `json:"body,omitempty"`
}

func renderDocsPage(entries []docs.Entry, query string, paging discoveryPaging, output string) error {
	filtered := make([]docs.Entry, 0, len(entries))
	for _, entry := range entries {
		if matchesDiscoveryQuery(query, entry.Slug+" "+entry.Title+" "+entry.Summary) {
			filtered = append(filtered, entry)
		}
	}
	sort.Slice(filtered, func(i, j int) bool { return filtered[i].Slug < filtered[j].Slug })
	keys := make([]string, len(filtered))
	for i, entry := range filtered {
		keys[i] = entry.Slug
	}
	start, end, page, err := paging.bounds(keys)
	if err != nil {
		return err
	}
	if err := renderDocsList(filtered[start:end], output); err != nil {
		return err
	}
	return page.write(output)
}

func renderDocsSections(hits []docs.Section, query string, withBody bool, output string) error {
	switch strings.ToLower(output) {
	case "json":

		encoder := json.NewEncoder(os.Stdout)
		for _, hit := range hits {
			row := sectionResult{Slug: hit.Slug, Heading: hit.Heading, Level: hit.Level, StartLine: hit.StartLine, EndLine: hit.EndLine, Breadcrumb: hit.Breadcrumb, Snippet: sectionSnippet(hit, query)}
			if withBody {
				row.Body = hit.Body
			}
			if err := encoder.Encode(row); err != nil {
				return err
			}
		}
		return nil
	case "plain":
		for _, h := range hits {
			fmt.Printf("%s:%d\t%s\n", h.Slug, h.StartLine, sectionLabel(h))
		}
		return nil
	case "pretty", "":
		if len(hits) == 0 {
			fmt.Printf("no section matches %q\n", query)
			fmt.Printf("%s sparkwing docs search --search %q --topics\n",
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
			fmt.Printf("  %s\n", color.Dim(sectionSnippet(h, query)))
		}
		if !withBody {
			fmt.Printf("%s %s\n", color.Dim("read them in full:"),
				color.Cyan("sparkwing docs read --topic <slug> --section <start_line>"))
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
	runes := []rune(s)
	if len(runes) <= max {
		return s
	}
	return string(runes[:max]) + "..."
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
