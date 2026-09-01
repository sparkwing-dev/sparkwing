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
	for _, p := range docFiles {
		b, err := os.ReadFile(p)
		if err != nil {
			t.Fatal(err)
		}
		for _, m := range qualified.FindAllStringSubmatch(string(b), -1) {
			if !syms[m[1]] {
				rel, _ := filepath.Rel(root, p)
				missing[m[1]] = append(missing[m[1]], rel)
			}
		}
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
