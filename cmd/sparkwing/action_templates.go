// `sparkwing examples` browses the sparks-core registry: complete,
// working pipelines to read rather than starting points to scaffold.
// `pipeline new --template <shape>` starts a pipeline; an example shows
// how a real one is built. --name switches to a detail view, --body
// prints the source, --category / --cloud filter.
package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"text/tabwriter"

	"github.com/sparkwing-dev/sparkwing/internal/ndjson"

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

// runExamples browses the worked-example corpus.
//
// These are working pipelines, not starting points. `pipeline new`
// scaffolds a shape; an example shows how a real one is built, and is
// read rather than copied wholesale -- which is why it lives under its
// own verb instead of behind --template, where choosing between forty
// of them was a quarter of an agent trial's turns.
func runExamples(args []string) error {
	if len(args) > 0 && args[0] == "scaffold" {
		return runExampleScaffold(args[1:])
	}
	fs := flag.NewFlagSet(cmdExamples.Path, flag.ContinueOnError)
	var output, category, cloud, name string
	var body bool
	fs.StringVarP(&output, "output", "o", "pretty", "pretty | json")
	fs.StringVar(&category, "category", "", "filter the list by applicability category")
	fs.StringVar(&cloud, "cloud", "", "filter the list by cloud (aws | gcp); cloud-agnostic templates always match")
	fs.StringVar(&name, "name", "", "show full detail for one template instead of the list")
	fs.BoolVar(&body, "body", false, "with --name, also print the rendered pipeline body")
	_ = chdirFlag(fs)
	if err := parseAndCheck(cmdExamples, fs, args); err != nil {
		if errors.Is(err, errHelpRequested) {
			return nil
		}
		return err
	}
	if fs.NArg() > 0 {
		return fmt.Errorf("examples: unexpected positional %q", fs.Arg(0))
	}
	if body && name == "" {
		return errors.New("examples: --body requires --name <example>")
	}

	if name != "" {
		return showTemplateDetail(name, body, output)
	}
	return listTemplates(category, cloud, output)
}

// listTemplates renders the registry, optionally filtered by category
// and cloud.
//
// A filter that matches nothing prints the values that do exist. The
// miss is nearly always a guessed filter, and without the list the only
// way to recover is to dump the unfiltered registry and reverse-engineer
// the vocabulary from it.
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
		// NDJSON: one example manifest per line.
		return ndjson.Write(os.Stdout, manifests)
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
// renderTemplateList prints one line per template: the name and the
// first line of when to reach for it.
//
// The full manifest for every template ran to six hundred lines, which
// is not a list anyone reads -- agent trials grepped it, re-dumped it as
// JSON, and parsed it with python before picking, four turns to answer
// "which one runs go test". Choosing needs the name and one line;
// everything else belongs behind --name.
//
// The built-in shapes are listed alongside, marked, because they were
// the other half of that cost: `ci-pr-check` is named for a job a
// registry template actually does, and reads as the answer until you
// open it and find echo placeholders.
func renderTemplateList(filtered []templates.Template) {
	groups := groupTemplatesByCategory(filtered)
	for i, g := range groups {
		if i > 0 {
			fmt.Println()
		}
		fmt.Println(color.Bold(strings.ToUpper(g.category)))
		for _, t := range g.templates {
			printTemplateLine(t.Manifest)
		}
	}
	printTemplateListFooter(len(filtered), len(groups))
}

// printTemplateLine is the one-line form: name, then the first sentence
// of whenToUse.
func printTemplateLine(m templates.Manifest) {
	signal := strings.TrimSpace(m.WhenToUse)
	if signal == "" {
		signal = strings.TrimSpace(m.Description)
	}
	signal = strings.Join(strings.Fields(signal), " ")
	if i := strings.Index(signal, ". "); i > 0 {
		signal = signal[:i]
	}
	const width = 30
	name := m.Name
	pad := ""
	if len(name) < width {
		pad = strings.Repeat(" ", width-len(name))
	}
	fmt.Printf("  %s%s %s\n", color.Bold(name), pad, color.Dim(truncateLine(signal)))
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
	printAlignedSteps([]InfoNextStep{
		{Command: "sparkwing docs search -q <what you are doing>", Purpose: "usually the faster way in than this list"},
		{Command: "sparkwing examples --name <example> --body", Purpose: "read one in full"},
		{Command: "sparkwing examples --category <c> --cloud <aws|gcp>", Purpose: "narrow the list"},
		{Command: "sparkwing pipeline new --name <n> --template <shape>", Purpose: "start a pipeline (a shape, not an example)"},
	})
	fmt.Printf("\n%s\n", color.Dim(fmt.Sprintf("%s across %s -- working pipelines to read, not starting points",
		countNoun(shown, "example", "examples"), countNoun(categories, "category", "categories"))))
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

// showTemplateDetail renders one template in full: manifest metadata,
// the parameters table, applicability, README, and -- with body -- the
// rendered pipeline body under default + placeholder parameter values.
func showTemplateDetail(name string, body bool, output string) error {
	tmpl, err := templates.Get(name)
	if err != nil {
		return fmt.Errorf("template %q not found -- run `sparkwing examples` to list them", name)
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
	printExampleFooter(body)
}

// printExampleFooter closes a worked example with what to do next.
//
// It cannot offer to scaffold this example: `pipeline new --template`
// takes a shape, and passing a registry name is now an error. Nor
// should it -- an example is a finished pipeline for someone else's
// repo, and starting from one means deleting its assumptions before
// writing your own. Read it, scaffold the shape it uses, write the
// bodies you need.
func printExampleFooter(body bool) {
	fmt.Println()
	if !body {
		fmt.Printf("%s sparkwing examples --name <name> --body\n",
			color.Dim("read the code:"))
	}
	fmt.Printf("%s sparkwing pipeline new --name <name> --template <shape>  (%s)\n",
		color.Dim("start your own:"), strings.Join(builtinShapeNames(), " | "))
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
// templateCategories returns every category the registry declares,
// sorted and deduplicated.
func templateCategories(list []templates.Template) []string {
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

// templateClouds returns every cloud the registry declares, sorted and
// deduplicated.
func templateClouds(list []templates.Template) []string {
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

// runExampleScaffold materializes an example into a repo.
//
// It exists so template-verify can keep proving every example
// compiles, lints, and explains (and that runnable-tier ones run) --
// the property that makes them worth
// reading. It is not the path to start a pipeline, which is why the
// verb is hidden and `pipeline new --template` no longer accepts an
// example name.
func runExampleScaffold(args []string) error {
	fs := flag.NewFlagSet(cmdExampleScaffold.Path, flag.ContinueOnError)
	name := fs.String("name", "", "example to materialize")
	params := fs.StringArray("param", nil, "example parameter, k=v (repeatable)")
	changeDir := fs.StringP("sw-cd", "C", "", "operate as if started in this directory")
	if err := parseAndCheck(cmdExampleScaffold, fs, args); err != nil {
		if errors.Is(err, errHelpRequested) {
			return nil
		}
		return err
	}
	if *name == "" {
		return errors.New("examples scaffold: --name is required")
	}
	if *changeDir != "" {
		if err := os.Chdir(*changeDir); err != nil {
			return fmt.Errorf("examples scaffold: --sw-cd %q: %w", *changeDir, err)
		}
	}
	cwd, err := os.Getwd()
	if err != nil {
		return err
	}
	sparkwingDir, ok := walkUpForSparkwing(cwd)
	bootstrapped := !ok
	if !ok {
		if err := bootstrapDotSparkwingOpts(cwd, filepath.Join(cwd, ".sparkwing"), true); err != nil {
			return err
		}
		sparkwingDir = filepath.Join(cwd, ".sparkwing")
	}
	return scaffoldFromRegistry(sparkwingDir, *name, *name, *params, false, bootstrapped)
}

// builtinShapeNames is the set `pipeline new --template` accepts.
func builtinShapeNames() []string {
	out := make([]string, 0, len(builtinShapes))
	for _, s := range builtinShapes {
		out = append(out, s.Name)
	}
	return out
}
