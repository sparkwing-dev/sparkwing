package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
)

// auxDocGlobs name the maintained markdown files outside docs/: the
// repo's root-level markdown and the satellite READMEs (charts/,
// install/, web/). These surfaces are read by the same users and agents
// as docs/ but have no other coupling to the code, so a rename the
// docs/ gates would catch could still rot them silently. Deliberately a
// glob list, not a walk: internal/ testdata contains fixture READMEs
// that are test inputs, not documentation.
var auxDocGlobs = []string{
	"*.md",
	"charts/*/README.md",
	"install/*/*/README.md",
	"web/*.md",
}

// auxDocFiles resolves auxDocGlobs under repoRoot, sorted and deduped.
// CHANGELOG.md is dropped: the changelog is a historical record that
// names removed flags and files because recording them is its job, the
// same reason migrations/ is excluded from the docs/ gates.
func auxDocFiles(repoRoot string) []string {
	seen := map[string]bool{}
	var out []string
	for _, g := range auxDocGlobs {
		matches, _ := filepath.Glob(filepath.Join(repoRoot, g))
		for _, m := range matches {
			if filepath.Base(m) == "CHANGELOG.md" {
				continue
			}
			if !seen[m] {
				seen[m] = true
				out = append(out, m)
			}
		}
	}
	sort.Strings(out)
	return out
}

// checkAuxDocs runs the drift subset of the doc gates over the aux
// docs: dead tokens and CLI-verb resolution over every file, plus
// relative .md-link resolution. The pipeline registrations under
// .sparkwing/ and examples/*.yaml are scanned for dead tokens and
// resolvable `sparkwing` invocations too -- pipeline help strings and
// example workflows are documentation with no other gate. Only the
// editorial checks (history narrative, frozen counts) stay docs/-only:
// VERSIONING.md documents the deprecation procedure and DESIGN-* docs
// record how the design evolved, so change vocabulary is their subject
// matter, not rot.
//
// A `sparkwing ...` command shown in a pipeline Example string, a help
// snippet, or a workflow step is documentation the reader will paste,
// so its verbs resolve like doc prose. Extraction is bounded to keep
// prose that merely mentions the word from being parsed as a command:
// backtick spans anywhere, YAML `run:` steps, and double-quoted
// literals only on Example `Command:` lines -- a quoted string that
// merely starts with "sparkwing" elsewhere is usually an error message
// about the product, not a command.
func checkAuxDocs(repoRoot string) bool {
	valid, posArgs, err := loadRegistry(repoRoot)
	if err != nil {
		fmt.Fprintln(os.Stderr, "aux-docs: load registry:", err)
		return false
	}

	mdFiles := auxDocFiles(repoRoot)
	goFiles, _ := filepath.Glob(filepath.Join(repoRoot, ".sparkwing", "*.go"))
	jobFiles, _ := filepath.Glob(filepath.Join(repoRoot, ".sparkwing", "jobs", "*.go"))
	yamlFiles, _ := filepath.Glob(filepath.Join(repoRoot, "examples", "*.yaml"))
	nonMD := append(append(append([]string{}, goFiles...), jobFiles...), yamlFiles...)

	var hits []string
	var verbs, links int

	for _, path := range mdFiles {
		data, rerr := os.ReadFile(path)
		if rerr != nil {
			fmt.Fprintln(os.Stderr, "aux-docs: read error:", rerr)
			return false
		}
		doc := string(data)
		rel, _ := filepath.Rel(repoRoot, path)

		for ln, line := range strings.Split(doc, "\n") {
			for _, b := range banned {
				if m := b.re.FindString(line); m != "" {
					hits = append(hits, fmt.Sprintf("%s:%d: dead token %q -- %s", rel, ln+1, m, b.want))
				}
			}
			for _, lm := range mdLinkRE.FindAllStringSubmatch(line, -1) {
				target := lm[1]
				if strings.HasPrefix(target, "http://") || strings.HasPrefix(target, "https://") ||
					strings.HasPrefix(target, "mailto:") || strings.HasPrefix(target, "#") {
					continue
				}
				if i := strings.IndexByte(target, '#'); i >= 0 {
					target = target[:i]
				}
				if target == "" || !strings.HasSuffix(target, ".md") {
					continue
				}
				links++
				if _, statErr := os.Stat(filepath.Join(filepath.Dir(path), target)); statErr != nil {
					hits = append(hits, fmt.Sprintf("%s:%d: broken link -> missing %s", rel, ln+1, target))
				}
			}
		}

		if unshippedDesignRE.Match(data) {
			continue
		}
		for _, inv := range extractInvocations(rel, doc) {
			verbs++
			if unknown := resolvePath(inv.tokens, valid, posArgs); unknown != "" {
				hits = append(hits, fmt.Sprintf("%s:%d: `%s` -- no such command %q", inv.file, inv.line, inv.raw, unknown))
			}
		}
	}

	for _, path := range nonMD {
		data, rerr := os.ReadFile(path)
		if rerr != nil {
			fmt.Fprintln(os.Stderr, "aux-docs: read error:", rerr)
			return false
		}
		rel, _ := filepath.Rel(repoRoot, path)
		for ln, line := range strings.Split(string(data), "\n") {
			for _, b := range banned {
				if m := b.re.FindString(line); m != "" {
					hits = append(hits, fmt.Sprintf("%s:%d: dead token %q -- %s", rel, ln+1, m, b.want))
				}
			}
			var candidates []string
			if strings.Contains(line, "Command:") {
				for _, m := range quotedCmdRE.FindAllStringSubmatch(line, -1) {
					candidates = append(candidates, m[1])
				}
			}
			for _, m := range inlineCodeRE.FindAllStringSubmatch(line, -1) {
				candidates = append(candidates, m[1])
			}
			if strings.HasSuffix(path, ".yaml") {
				if m := yamlRunCmdRE.FindStringSubmatch(line); m != nil {
					candidates = append(candidates, m[1])
				}
			}
			for _, c := range candidates {
				inv, ok := parseInvocation(rel, ln+1, c)
				if !ok {
					continue
				}
				verbs++
				if unknown := resolvePath(inv.tokens, valid, posArgs); unknown != "" {
					hits = append(hits, fmt.Sprintf("%s:%d: `%s` -- no such command %q", inv.file, inv.line, inv.raw, unknown))
				}
			}
		}
	}

	fmt.Printf("doccheck/aux-docs: %d markdown + %d pipeline/example file(s), %d invocation(s), %d link(s) -- %d hit(s)\n",
		len(mdFiles), len(nonMD), verbs, links, len(hits))
	if len(hits) > 0 {
		fmt.Printf("\n%d aux-doc problem(s):\n", len(hits))
		for _, h := range hits {
			fmt.Printf("  %s\n", h)
		}
		return false
	}
	fmt.Printf("\nNO DRIFT IN AUX DOCS, PIPELINE HELP, OR EXAMPLES\n")
	return true
}

var (
	// quotedCmdRE lifts a double-quoted `sparkwing ...` literal out of a
	// Go or YAML source line.
	quotedCmdRE = regexp.MustCompile(`"(sparkwing [^"]*)"`)
	// yamlRunCmdRE lifts the command from a YAML `run:` step.
	yamlRunCmdRE = regexp.MustCompile(`run:\s*(sparkwing\s.*)$`)
)

// sidebar mirrors docs/_sidebar.json: the categories the site renders
// and the entries deliberately kept out of navigation.
type sidebar struct {
	Categories []struct {
		Label string   `json:"label"`
		Slugs []string `json:"slugs"`
	} `json:"categories"`
	Excluded []string `json:"excluded"`
}

// checkSidebar verifies docs/_sidebar.json against the docs tree in both
// directions: every listed slug must exist as a page, and every page
// must be either listed or explicitly excluded. The links gate catches a
// renamed page's dangling references, but nothing else notices a new
// page that never gets wired into navigation -- it would be published
// yet unreachable. A stale exclusion is drift too: an entry naming a
// directory or page that no longer exists reads as deliberate curation
// but guards nothing.
func checkSidebar(contentDir string) bool {
	data, err := os.ReadFile(filepath.Join(contentDir, "_sidebar.json"))
	if err != nil {
		fmt.Fprintln(os.Stderr, "sidebar: read:", err)
		return false
	}
	var sb sidebar
	if err := json.Unmarshal(data, &sb); err != nil {
		fmt.Fprintln(os.Stderr, "sidebar: parse:", err)
		return false
	}

	listed := map[string]bool{}
	var problems []string
	for _, c := range sb.Categories {
		for _, s := range c.Slugs {
			listed[s] = true
			if _, statErr := os.Stat(filepath.Join(contentDir, s+".md")); statErr != nil {
				problems = append(problems, fmt.Sprintf("category %q lists %q but docs/%s.md does not exist", c.Label, s, s))
			}
		}
	}

	excluded := func(slug string) bool {
		for _, e := range sb.Excluded {
			if slug == e || (strings.HasSuffix(e, "/") && strings.HasPrefix(slug, e)) {
				return true
			}
		}
		return false
	}

	for _, e := range sb.Excluded {
		target := filepath.Join(contentDir, strings.TrimSuffix(e, "/"))
		if !strings.HasSuffix(e, "/") {
			target += ".md"
		}
		if _, statErr := os.Stat(target); statErr != nil {
			problems = append(problems, fmt.Sprintf("excluded entry %q names nothing in docs/ -- stale exclusion", e))
		}
	}

	entries, err := os.ReadDir(contentDir)
	if err != nil {
		fmt.Fprintln(os.Stderr, "sidebar: readdir:", err)
		return false
	}
	var pages int
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".md") || e.Name() == "README.md" {
			continue
		}
		pages++
		slug := strings.TrimSuffix(e.Name(), ".md")
		if !listed[slug] && !excluded(slug) {
			problems = append(problems, fmt.Sprintf("docs/%s.md is neither in a sidebar category nor in \"excluded\" -- unreachable from navigation", slug))
		}
	}

	fmt.Printf("doccheck/sidebar: %d page(s) vs %d listed slug(s) -- %d problem(s)\n", pages, len(listed), len(problems))
	if len(problems) > 0 {
		fmt.Printf("\n%d sidebar problem(s):\n", len(problems))
		for _, p := range problems {
			fmt.Printf("  %s\n", p)
		}
		return false
	}
	fmt.Printf("\nSIDEBAR AND DOCS TREE AGREE\n")
	return true
}
