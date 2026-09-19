package docs_test

import (
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"
)

func TestDocsNameOnlySymbolsThatExist(t *testing.T) {
	root := repoRoot(t)
	syms := exportedSymbols(t, filepath.Join(root, "sparkwing"))
	if len(syms) < 100 {
		t.Fatalf("harvested only %d SDK symbols; the scan would pass vacuously", len(syms))
	}

	qualified := regexp.MustCompile(`\b(?:sw|sparkwing)\.([A-Z]\w*)`)

	var docFiles []string
	err := filepath.Walk(filepath.Join(root, "docs"), func(p string, info os.FileInfo, err error) error {
		if err != nil || info.IsDir() || !strings.HasSuffix(p, ".md") {
			return err
		}
		if strings.Contains(p, "/migrations/") || strings.Contains(p, "/proposals/") {
			return nil
		}
		docFiles = append(docFiles, p)
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(docFiles) == 0 {
		t.Fatal("found no docs to check")
	}

	missing := map[string][]string{}
	harvested := 0
	for _, p := range docFiles {
		b, err := os.ReadFile(p)
		if err != nil {
			t.Fatal(err)
		}
		rel, _ := filepath.Rel(root, p)
		for _, m := range qualified.FindAllStringSubmatch(string(b), -1) {
			if !syms[m[1]] {
				missing[m[1]] = append(missing[m[1]], rel)
			}
		}
		// safety: an API summary block lists its names bare, so the qualified
		// pattern above cannot see the one place the surface is enumerated.
		bare := bareAPIBlockNames(string(b))
		harvested += len(bare)
		for _, name := range bare {
			if !syms[name] {
				missing[name] = append(missing[name], rel)
			}
		}
	}
	// safety: the harvest is the assertion's input, so a scan that stops
	// finding names passes while checking nothing.
	if harvested < 20 {
		t.Fatalf("harvested only %d bare API names; the scan would pass vacuously", harvested)
	}
	names := make([]string, 0, len(missing))
	for n := range missing {
		names = append(names, n)
	}
	sort.Strings(names)
	for _, n := range names {
		t.Errorf("docs reference sparkwing.%s, which the SDK does not export (%s)",
			n, strings.Join(dedupe(missing[n]), ", "))
	}
}

func bareAPIBlockNames(doc string) []string {
	call := regexp.MustCompile(`^([A-Z]\w*)\(`)
	var names []string
	inFence := false
	for _, line := range strings.Split(doc, "\n") {
		if strings.HasPrefix(line, "```") {
			inFence = !inFence
			continue
		}
		if !inFence {
			continue
		}
		if m := call.FindStringSubmatch(strings.TrimSpace(line)); m != nil {
			names = append(names, m[1])
		}
	}
	return names
}

func exportedSymbols(t *testing.T, dir string) map[string]bool {
	t.Helper()
	patterns := []*regexp.Regexp{
		regexp.MustCompile(`(?m)^func\s+(?:\([^)]*\)\s*)?([A-Z]\w*)`),
		regexp.MustCompile(`(?m)^type\s+([A-Z]\w*)`),
		regexp.MustCompile(`(?m)^(?:const|var)\s+\(?\s*([A-Z]\w*)`),
		regexp.MustCompile(`(?m)^\t([A-Z]\w*)\b`),
	}
	out := map[string]bool{}
	err := filepath.Walk(dir, func(p string, info os.FileInfo, err error) error {
		if err != nil || info.IsDir() || !strings.HasSuffix(p, ".go") || strings.HasSuffix(p, "_test.go") {
			return err
		}
		b, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		for _, re := range patterns {
			for _, m := range re.FindAllStringSubmatch(string(b), -1) {
				out[m[1]] = true
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return out
}

func dedupe(in []string) []string {
	seen := map[string]bool{}
	var out []string
	for _, s := range in {
		if !seen[s] {
			seen[s] = true
			out = append(out, s)
		}
	}
	return out
}
