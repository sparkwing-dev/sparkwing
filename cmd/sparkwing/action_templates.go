package main

import (
	"encoding/json"
	"fmt"
	"os"
	"sort"
	"strings"
	"text/tabwriter"

	"github.com/sparkwing-dev/sparkwing/internal/ndjson"
	"github.com/sparkwing-dev/sparkwing/sparkwing/protocol/apierr"

	"github.com/sparkwing-dev/sparkwing/internal/clisurface"

	"github.com/sparkwing-dev/sparkwing/pkg/color"
	tsexamples "github.com/sparkwing-dev/sparkwing/sdk/typescript/examples"

	templates "github.com/sparkwing-dev/sparks-core/templates"
)

const uncategorizedLabel = "uncategorized"

const (
	exampleLanguageGo         = "go"
	exampleLanguageTypeScript = "typescript"
)

type templateDetailJSON struct {
	Manifest     templates.Manifest `json:"manifest"`
	Language     string             `json:"language"`
	ReadMe       string             `json:"readme,omitempty"`
	RenderedBody string             `json:"rendered_body,omitempty"`
}

// exampleEntry is one listed example: a sparks-core Go template or a
// TypeScript example embedded from sdk/typescript/examples.
type exampleEntry struct {
	templates.Template
	Language string
}

func exampleCatalog() ([]exampleEntry, error) {
	goList, err := templates.List()
	if err != nil {
		return nil, fmt.Errorf("load templates: %w", err)
	}
	tsList, err := tsexamples.List()
	if err != nil {
		return nil, fmt.Errorf("load TypeScript examples: %w", err)
	}
	out := make([]exampleEntry, 0, len(goList)+len(tsList))
	for _, t := range goList {
		out = append(out, exampleEntry{Template: t, Language: exampleLanguageGo})
	}
	for _, e := range tsList {
		out = append(out, exampleEntry{Language: exampleLanguageTypeScript, Template: templates.Template{
			Manifest: templates.Manifest{
				Name:          e.Name,
				Description:   e.Description,
				WhenToUse:     e.WhenToUse,
				Prerequisite:  e.Prerequisite,
				Applicability: templates.Applicability{Category: e.Category},
			},
			ReadMe: e.ReadMe,
			Body:   e.Source,
		}})
	}
	return out, nil
}

func findExample(name string) (exampleEntry, error) {
	list, err := exampleCatalog()
	if err != nil {
		return exampleEntry{}, err
	}
	for _, e := range list {
		if e.Manifest.Name == name {
			return e, nil
		}
	}
	return exampleEntry{}, &apierr.Error{Code: apierr.NotFound, Message: fmt.Sprintf("template %q not found -- run `sparkwing examples` to list them", name)}
}

func runExamples(inv clisurface.Invocation) error {
	output := inv.Output(color.IsInteractiveStdout())
	if inv.Help() {
		return writeCommandHelp(surfaceHelp(inv.Command), os.Stdout, output)
	}
	if name := inv.Value("example"); name != "" {
		return showTemplateDetail(name, inv.Bool("body"), output)
	}
	return listTemplates(inv.Value("category"), inv.Value("cloud"), inv.Value("language"), output)
}

func listTemplates(category, cloud, language, output string) error {
	switch language = strings.ToLower(strings.TrimSpace(language)); language {
	case "", exampleLanguageGo, exampleLanguageTypeScript:
	default:
		return fmt.Errorf("unknown language %q (valid: go, typescript)", language)
	}
	list, err := exampleCatalog()
	if err != nil {
		return err
	}
	filtered := make([]exampleEntry, 0, len(list))
	for _, t := range list {
		if templateMatchesCategory(t.Manifest, category) && templateMatchesCloud(t.Manifest, cloud) && (language == "" || t.Language == language) {
			filtered = append(filtered, t)
		}
	}

	switch strings.ToLower(output) {
	case "json":
		records := make([]templateListRecord, 0, len(filtered))
		for _, t := range filtered {
			records = append(records, templateListRecord{Name: t.Manifest.Name, Category: templateCategory(t.Manifest), Summary: templateSummary(t.Manifest), Language: t.Language})
		}
		return ndjson.Write(os.Stdout, records)
	case "plain":
		for _, t := range filtered {
			if _, err := fmt.Fprintln(os.Stdout, t.Manifest.Name); err != nil {
				return err
			}
		}
		return nil
	case "pretty", "":
		if len(filtered) == 0 {
			fmt.Println("no templates match the given filters")
			if cats := templateCategories(list); category != "" && len(cats) > 0 {
				fmt.Printf("%s %s\n", color.Dim("categories:"), strings.Join(cats, ", "))
			}
			if clouds := templateClouds(list); cloud != "" && len(clouds) > 0 {
				fmt.Printf("%s %s\n", color.Dim("clouds:"), strings.Join(clouds, ", "))
			}
			fmt.Printf("%s sparkwing examples%s\n",
				color.Dim("browse all:"), clearedFilterSuffix(category, cloud, language))
			return nil
		}
		renderTemplateList(filtered)
		return nil
	default:
		return fmt.Errorf("unknown output format %q (valid: pretty, json, plain)", output)
	}
}

func renderTemplateList(filtered []exampleEntry) {
	groups := groupTemplatesByCategory(filtered)
	for i, g := range groups {
		if i > 0 {
			fmt.Println()
		}
		fmt.Println(color.Bold(strings.ToUpper(g.category)))
		for _, t := range g.templates {
			printTemplateLine(t)
		}
	}
	printTemplateListFooter(len(filtered), len(groups))
}

type templateListRecord struct {
	Name     string `json:"name"`
	Category string `json:"category"`
	Summary  string `json:"summary"`
	Language string `json:"language"`
}

func templateSummary(m templates.Manifest) string {
	signal := strings.TrimSpace(m.WhenToUse)
	if signal == "" {
		signal = strings.TrimSpace(m.Description)
	}
	signal = strings.Join(strings.Fields(signal), " ")
	if i := strings.Index(signal, ". "); i > 0 {
		signal = signal[:i]
	}
	return signal
}

func templateCategory(m templates.Manifest) string {
	if cat := strings.TrimSpace(m.Applicability.Category); cat != "" {
		return cat
	}
	return uncategorizedLabel
}

func printTemplateLine(e exampleEntry) {
	m := e.Manifest
	signal := templateSummary(m)
	if e.Language == exampleLanguageTypeScript {
		signal = "[typescript] " + signal
	}
	const width = 30
	name := m.Name
	pad := ""
	if len(name) < width {
		pad = strings.Repeat(" ", width-len(name))
	}
	fmt.Printf("  %s%s %s\n", color.Bold(name), pad, color.Dim(truncateLine(signal)))
}

type templateCategoryGroup struct {
	category  string
	templates []exampleEntry
}

func groupTemplatesByCategory(list []exampleEntry) []templateCategoryGroup {
	order := make([]string, 0)
	byCat := make(map[string][]exampleEntry)
	for _, t := range list {
		cat := templateCategory(t.Manifest)
		if _, seen := byCat[cat]; !seen {
			order = append(order, cat)
		}
		byCat[cat] = append(byCat[cat], t)
	}
	sort.SliceStable(order, func(i, j int) bool {
		a, b := order[i], order[j]
		if a == uncategorizedLabel {
			return false
		}
		if b == uncategorizedLabel {
			return true
		}
		return a < b
	})
	groups := make([]templateCategoryGroup, 0, len(order))
	for _, cat := range order {
		groups = append(groups, templateCategoryGroup{category: cat, templates: byCat[cat]})
	}
	return groups
}

func printTemplateListFooter(shown, categories int) {
	fmt.Println()
	printAlignedSteps([]InfoNextStep{
		{Command: "sparkwing docs search --search <what you are doing>", Purpose: "usually the faster way in than this list"},
		{Command: "sparkwing examples --example <example> --body", Purpose: "read one in full"},
		{Command: "sparkwing examples --category <c> --cloud <aws|gcp>", Purpose: "narrow the list"},
		{Command: "sparkwing examples --language typescript", Purpose: "only TypeScript pipelines"},
		{Command: "sparkwing pipeline new --pipeline <n> --template <shape>", Purpose: "start a pipeline (a shape, not an example)"},
	})
	fmt.Printf("\n%s\n", color.Dim(fmt.Sprintf("%s across %s -- working pipelines to read, not starting points",
		countNoun(shown, "example", "examples"), countNoun(categories, "category", "categories"))))
}

func countNoun(n int, singular, plural string) string {
	if n == 1 {
		return fmt.Sprintf("%d %s", n, singular)
	}
	return fmt.Sprintf("%d %s", n, plural)
}

func clearedFilterSuffix(category, cloud, language string) string {
	var parts []string
	if strings.TrimSpace(language) != "" {
		parts = append(parts, "--language "+language)
	}
	if strings.TrimSpace(category) != "" {
		parts = append(parts, "--category "+category)
	}
	if strings.TrimSpace(cloud) != "" {
		parts = append(parts, "--cloud "+cloud)
	}
	if len(parts) == 0 {
		return ""
	}
	return color.Dim(" (without " + strings.Join(parts, " ") + ")")
}

func showTemplateDetail(name string, body bool, output string) error {
	entry, err := findExample(name)
	if err != nil {
		return err
	}
	tmpl := entry.Template
	var rendered string
	if body {
		rendered = tmpl.Body
		if entry.Language == exampleLanguageGo {
			rendered, err = renderTemplateWithPlaceholders(tmpl)
			if err != nil {
				return fmt.Errorf("render body: %w", err)
			}
		}
	}

	switch strings.ToLower(output) {
	case "json":
		out := templateDetailJSON{Manifest: tmpl.Manifest, Language: entry.Language, ReadMe: tmpl.ReadMe}
		if body {
			out.RenderedBody = rendered
		}
		enc := json.NewEncoder(os.Stdout)
		return enc.Encode(out)
	case "plain":
		if body {
			_, err = fmt.Fprintln(os.Stdout, rendered)
		} else {
			_, err = fmt.Fprintln(os.Stdout, tmpl.Manifest.Name)
		}
		return err
	case "pretty", "":
		printTemplateDetail(entry, rendered, body)
		return nil
	default:
		return fmt.Errorf("unknown output format %q (valid: pretty, json, plain)", output)
	}
}

func printTemplateDetail(entry exampleEntry, rendered string, body bool) {
	tmpl := entry.Template
	m := tmpl.Manifest
	fmt.Println(color.Bold(m.Name))
	fmt.Printf("%s %s\n", color.Bold("language:"), entry.Language)
	if desc := strings.TrimSpace(m.Description); desc != "" {
		fmt.Printf("\n%s\n", desc)
	}
	if when := strings.TrimSpace(m.WhenToUse); when != "" {
		fmt.Printf("\n%s\n%s\n", color.Bold("when to use:"), when)
	}
	if pre := strings.TrimSpace(m.Prerequisite); pre != "" {
		fmt.Printf("\n%s %s\n", color.Bold("prerequisite:"), pre)
	}
	if applies := applicabilityLine(m.Applicability); applies != "" {
		fmt.Printf("\n%s %s\n", color.Bold("applicability:"), applies)
	}
	if len(m.Parameters) > 0 {
		fmt.Printf("\n%s\n", color.Bold("parameters:"))
		tw := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
		fmt.Fprintln(tw, "  NAME\tTYPE\tREQUIRED\tDEFAULT\tDESCRIPTION")
		for _, p := range m.Parameters {
			typ := p.Type
			if typ == "" {
				typ = "string"
			}
			required := "no"
			if p.Required {
				required = "yes"
			}
			dflt := p.Default
			if dflt == "" {
				dflt = "-"
			}
			desc := strings.ReplaceAll(strings.TrimSpace(p.Description), "\n", " ")
			fmt.Fprintf(tw, "  %s\t%s\t%s\t%s\t%s\n", p.Name, typ, required, dflt, desc)
		}
		tw.Flush()
	}
	if readme := strings.TrimSpace(tmpl.ReadMe); readme != "" {
		fmt.Printf("\n%s\n\n%s\n", color.Bold("README"), readme)
	}
	if body {
		heading := "rendered body (default + <placeholder> params):"
		if entry.Language == exampleLanguageTypeScript {
			heading = "source:"
		}
		fmt.Printf("\n%s\n", color.Bold(heading))
		fmt.Printf("\n%s\n", rendered)
	}
	printExampleFooter(body)
}

func printExampleFooter(body bool) {
	fmt.Println()
	if !body {
		fmt.Printf("%s sparkwing examples --example <name> --body\n",
			color.Dim("read the code:"))
	}
	fmt.Printf("%s sparkwing pipeline new --pipeline <name> --template <shape>  (%s)\n",
		color.Dim("start your own:"), strings.Join(builtinShapeNames(), " | "))
}

func renderTemplateWithPlaceholders(tmpl templates.Template) (string, error) {
	params := map[string]string{}
	for _, p := range tmpl.Manifest.Parameters {
		if p.Required && p.Default == "" {
			params[p.Name] = "<" + p.Name + ">"
		}
	}
	return templates.Render(tmpl.Manifest.Name, params)
}

func templateCategories(list []exampleEntry) []string {
	seen := map[string]bool{}
	var out []string
	for _, t := range list {
		if c := strings.TrimSpace(t.Manifest.Applicability.Category); c != "" && !seen[c] {
			seen[c] = true
			out = append(out, c)
		}
	}
	sort.Strings(out)
	return out
}

func templateClouds(list []exampleEntry) []string {
	seen := map[string]bool{}
	var out []string
	for _, t := range list {
		for _, c := range t.Manifest.Applicability.Cloud {
			if c = strings.TrimSpace(c); c != "" && !seen[c] {
				seen[c] = true
				out = append(out, c)
			}
		}
	}
	sort.Strings(out)
	return out
}

func templateMatchesCategory(m templates.Manifest, category string) bool {
	if category == "" {
		return true
	}
	return strings.EqualFold(strings.TrimSpace(m.Applicability.Category), strings.TrimSpace(category))
}

func templateMatchesCloud(m templates.Manifest, cloud string) bool {
	if cloud == "" {
		return true
	}
	if len(m.Applicability.Cloud) == 0 {
		return true
	}
	for _, c := range m.Applicability.Cloud {
		if strings.EqualFold(strings.TrimSpace(c), strings.TrimSpace(cloud)) {
			return true
		}
	}
	return false
}

func applicabilityLine(a templates.Applicability) string {
	var parts []string
	if cat := strings.TrimSpace(a.Category); cat != "" {
		parts = append(parts, "category "+cat)
	}
	if len(a.Cloud) > 0 {
		parts = append(parts, "cloud "+strings.Join(a.Cloud, ", "))
	} else {
		parts = append(parts, "cloud-agnostic")
	}
	return strings.Join(parts, "  ")
}

func builtinShapeNames() []string {
	out := make([]string, 0, len(builtinShapes))
	for _, s := range builtinShapes {
		out = append(out, s.Name)
	}
	return out
}
