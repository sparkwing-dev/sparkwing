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

var auxDocGlobs = []string{
	"*.md",
	"charts/*/README.md",
	"install/*/*/README.md",
	"web/*.md",
}

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
	quotedCmdRE = regexp.MustCompile(`"(sparkwing [^"]*)"`)

	yamlRunCmdRE = regexp.MustCompile(`run:\s*(sparkwing\s.*)$`)
)

type sidebar struct {
	Categories []struct {
		Label string   `json:"label"`
		Slugs []string `json:"slugs"`
	} `json:"categories"`
	Excluded []string `json:"excluded"`
}

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
