// `sparkwing pipeline templates` lists the sparks-core template registry
// -- the curated, parameterized pipeline starters that `pipeline new
// --template <name>` renders. Distinct from the two built-in stubs
// (minimal, build-test-deploy): those ship in this binary; these come
// from the sparks-core/templates module. --name switches to a detail
// view; --category / --cloud filter the list.
package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"sort"
	"strings"
	"text/tabwriter"

	flag "github.com/spf13/pflag"

	"github.com/sparkwing-dev/sparkwing/pkg/color"

	templates "github.com/sparkwing-dev/sparks-core/templates"
)

// uncategorizedLabel is the bucket header for templates whose manifest
// declares no applicability category.
const uncategorizedLabel = "uncategorized"

// templateDetailJSON is the -o json shape for the --name detail view.
// RenderedBody is populated only when --body is passed.
type templateDetailJSON struct {
	Manifest     templates.Manifest `json:"manifest"`
	ReadMe       string             `json:"readme,omitempty"`
	RenderedBody string             `json:"rendered_body,omitempty"`
}

func runPipelineTemplates(args []string) error {
	fs := flag.NewFlagSet(cmdPipelineTemplates.Path, flag.ContinueOnError)
	var output, category, cloud, name string
	var body bool
	fs.StringVarP(&output, "output", "o", "pretty", "pretty | json")
	fs.StringVar(&category, "category", "", "filter the list by applicability category")
	fs.StringVar(&cloud, "cloud", "", "filter the list by cloud (aws | gcp); cloud-agnostic templates always match")
	fs.StringVar(&name, "name", "", "show full detail for one template instead of the list")
	fs.BoolVar(&body, "body", false, "with --name, also print the rendered pipeline body")
	_ = chdirFlag(fs)
	if err := parseAndCheck(cmdPipelineTemplates, fs, args); err != nil {
		if errors.Is(err, errHelpRequested) {
			return nil
		}
		return err
	}
	if fs.NArg() > 0 {
		return fmt.Errorf("templates: unexpected positional %q", fs.Arg(0))
	}
	if body && name == "" {
		return errors.New("templates: --body requires --name <template>")
	}

	if name != "" {
		return showTemplateDetail(name, body, output)
	}
	return listTemplates(category, cloud, output)
}

// listTemplates renders the registry, optionally filtered by category
// and cloud.
func listTemplates(category, cloud, output string) error {
	list, err := templates.List()
	if err != nil {
		return fmt.Errorf("load templates: %w", err)
	}
	filtered := make([]templates.Template, 0, len(list))
	for _, t := range list {
		if templateMatchesCategory(t.Manifest, category) && templateMatchesCloud(t.Manifest, cloud) {
			filtered = append(filtered, t)
		}
	}

	switch strings.ToLower(output) {
	case "json":
		manifests := make([]templates.Manifest, 0, len(filtered))
		for _, t := range filtered {
			manifests = append(manifests, t.Manifest)
		}
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		return enc.Encode(manifests)
	case "pretty", "":
		if len(filtered) == 0 {
			fmt.Println("no templates match the given filters")
			fmt.Printf("%s sparkwing pipeline templates%s\n",
				color.Dim("browse all:"), clearedFilterSuffix(category, cloud))
			return nil
		}
		renderTemplateList(filtered)
		return nil
	default:
		return fmt.Errorf("unknown output format %q (valid: pretty, json)", output)
	}
}

// renderTemplateList prints the pretty catalog: templates grouped under
// category headers, followed by the affordance footer.
func renderTemplateList(filtered []templates.Template) {
	groups := groupTemplatesByCategory(filtered)
	for i, g := range groups {
		if i > 0 {
			fmt.Println()
		}
		fmt.Println(color.Bold(strings.ToUpper(g.category)))
		for _, t := range g.templates {
			printTemplateSummary(t.Manifest)
		}
	}
	printTemplateListFooter(len(filtered), len(groups))
}

// templateCategoryGroup is one category header plus the templates filed
// under it, as rendered by the pretty list.
type templateCategoryGroup struct {
	category  string
	templates []templates.Template
}

// groupTemplatesByCategory buckets templates by their applicability
// category, preserving each template's incoming order within a bucket.
// Categories sort alphabetically; the uncategorized bucket sorts last.
func groupTemplatesByCategory(list []templates.Template) []templateCategoryGroup {
	order := make([]string, 0)
	byCat := make(map[string][]templates.Template)
	for _, t := range list {
		cat := strings.TrimSpace(t.Manifest.Applicability.Category)
		if cat == "" {
			cat = uncategorizedLabel
		}
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

// printTemplateListFooter advertises the list's own affordances: the
// counts just shown, the narrowing filters, the detail view, and the
// scaffold command. It mirrors the tip footers other verbs print so a
// reader never has to grep the raw list to discover the flags.
func printTemplateListFooter(shown, categories int) {
	fmt.Println()
	labels := []struct{ tag, cmd string }{
		{"shown:", fmt.Sprintf("%s across %s", countNoun(shown, "template", "templates"), countNoun(categories, "category", "categories"))},
		{"filter:", "sparkwing pipeline templates --category <category> --cloud <aws|gcp>"},
		{"detail:", "sparkwing pipeline templates --name <template> [--body]"},
		{"scaffold:", "sparkwing pipeline new --name <name> --template <template> --param k=v ..."},
	}
	width := 0
	for _, l := range labels {
		if n := len(l.tag); n > width {
			width = n
		}
	}
	for _, l := range labels {
		pad := strings.Repeat(" ", width-len(l.tag))
		fmt.Printf("%s%s %s\n", color.Dim(l.tag), pad, l.cmd)
	}
}

// countNoun formats a count with the singular or plural noun.
func countNoun(n int, singular, plural string) string {
	if n == 1 {
		return fmt.Sprintf("%d %s", n, singular)
	}
	return fmt.Sprintf("%d %s", n, plural)
}

// clearedFilterSuffix echoes back the filters that produced an empty
// list so the no-match hint shows what to drop.
func clearedFilterSuffix(category, cloud string) string {
	var parts []string
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

// printTemplateSummary renders one template's catalog row: name, the
// "when to use" signal, its parameters, applicability, and prerequisite.
func printTemplateSummary(m templates.Manifest) {
	fmt.Println(color.Bold(m.Name))
	signal := strings.TrimSpace(m.WhenToUse)
	if signal == "" {
		signal = strings.TrimSpace(m.Description)
	}
	for _, line := range strings.Split(signal, "\n") {
		if line = strings.TrimSpace(line); line != "" {
			fmt.Printf("  %s\n", color.Dim(line))
		}
	}
	var req, opt []string
	for _, p := range m.Parameters {
		if p.Required {
			req = append(req, p.Name)
		} else {
			opt = append(opt, p.Name)
		}
	}
	if len(req) > 0 {
		fmt.Printf("  %s %s\n", color.Bold("required:"), strings.Join(req, ", "))
	}
	if len(opt) > 0 {
		fmt.Printf("  %s %s\n", color.Dim("optional:"), color.Dim(strings.Join(opt, ", ")))
	}
	if applies := applicabilityLine(m.Applicability); applies != "" {
		fmt.Printf("  %s %s\n", color.Dim("applies:"), color.Dim(applies))
	}
	if pre := strings.TrimSpace(m.Prerequisite); pre != "" {
		fmt.Printf("  %s %s\n", color.Bold("prerequisite:"), pre)
	}
	fmt.Println()
}

// showTemplateDetail renders one template in full: manifest metadata,
// the parameters table, applicability, README, and -- with body -- the
// rendered pipeline body under default + placeholder parameter values.
func showTemplateDetail(name string, body bool, output string) error {
	tmpl, err := templates.Get(name)
	if err != nil {
		return fmt.Errorf("template %q not found -- run `sparkwing pipeline templates` to list available templates", name)
	}
	var rendered string
	if body {
		rendered, err = renderTemplateWithPlaceholders(tmpl)
		if err != nil {
			return fmt.Errorf("render body: %w", err)
		}
	}

	switch strings.ToLower(output) {
	case "json":
		out := templateDetailJSON{Manifest: tmpl.Manifest, ReadMe: tmpl.ReadMe}
		if body {
			out.RenderedBody = rendered
		}
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		return enc.Encode(out)
	case "pretty", "":
		printTemplateDetail(tmpl, rendered, body)
		return nil
	default:
		return fmt.Errorf("unknown output format %q (valid: pretty, json)", output)
	}
}

func printTemplateDetail(tmpl templates.Template, rendered string, body bool) {
	m := tmpl.Manifest
	fmt.Println(color.Bold(m.Name))
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
		fmt.Printf("\n%s\n", color.Bold("rendered body (default + <placeholder> params):"))
		fmt.Printf("\n%s\n", rendered)
	}
	fmt.Printf("\n%s sparkwing pipeline new --name <name> --template %s --param k=v ...\n",
		color.Dim("scaffold:"), m.Name)
}

// renderTemplateWithPlaceholders renders the template body using the
// manifest defaults, synthesizing `<param>` placeholders for required
// parameters that declare no default so Render (which errors on a
// missing required param) succeeds for a preview.
func renderTemplateWithPlaceholders(tmpl templates.Template) (string, error) {
	params := map[string]string{}
	for _, p := range tmpl.Manifest.Parameters {
		if p.Required && p.Default == "" {
			params[p.Name] = "<" + p.Name + ">"
		}
	}
	return templates.Render(tmpl.Manifest.Name, params)
}

// templateMatchesCategory reports whether m passes the --category
// filter. An empty filter matches everything.
func templateMatchesCategory(m templates.Manifest, category string) bool {
	if category == "" {
		return true
	}
	return strings.EqualFold(strings.TrimSpace(m.Applicability.Category), strings.TrimSpace(category))
}

// templateMatchesCloud reports whether m passes the --cloud filter. An
// empty filter matches everything; a template that declares no cloud is
// cloud-agnostic and matches every cloud filter.
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

// applicabilityLine formats the applicability metadata as a single
// human-readable string, or "" when nothing is declared.
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
