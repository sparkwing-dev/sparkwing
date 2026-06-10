package main

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

// frozenCountRE catches a number word glued to an enumeration noun for
// an *open, code-defined set* -- "three places", "four checks". Those
// are open/closed violations in prose: the count is wrong the moment
// the code grows the set, and nobody rereads the sentence. The fix is
// to describe the mechanism (or point at a generated list), not tally.
//
// The noun list is deliberately narrow: only sets the code can extend.
// Invariant counts ("two-layer DAG", "two modes of X") use nouns kept
// off this list (layer, mode, ...) because stating them is fine.
var frozenCountRE = regexp.MustCompile(`(?i)\b(?:one|two|three|four|five|six|seven|eight|nine|ten|[0-9]+)[ -](places|ways|kinds|sources|triggers|checks|reasons|steps|stages|backends)\b`)

// generatedDocs are produced by a generator, not hand-authored; their
// wording comes from code (command descriptions, struct godoc), so the
// frozen-count rule -- a prose-style guideline -- doesn't apply.
var generatedDocs = map[string]bool{
	"cli-reference.md":    true,
	"config-reference.md": true,
}

// checkFrozenCounts flags hand-written docs that snapshot the size of an
// open set. Returns false on any hit.
func checkFrozenCounts(contentDir string) bool {
	var hits []string
	_ = filepath.Walk(contentDir, func(path string, info os.FileInfo, err error) error {
		if err != nil || info.IsDir() || !strings.HasSuffix(path, ".md") {
			return err
		}
		if strings.Contains(path, "/migrations/") || strings.Contains(path, "/proposals/") {
			return nil
		}
		if generatedDocs[filepath.Base(path)] {
			return nil
		}
		data, rerr := os.ReadFile(path)
		if rerr != nil {
			return rerr
		}
		rel, _ := filepath.Rel(contentDir, path)
		for ln, line := range strings.Split(string(data), "\n") {
			if m := frozenCountRE.FindString(line); m != "" {
				hits = append(hits, fmt.Sprintf("%s:%d: %q", rel, ln+1, strings.TrimSpace(m)))
			}
		}
		return nil
	})

	fmt.Printf("doccheck/frozen-counts: scanned hand-written docs -- %d hit(s)\n", len(hits))
	if len(hits) > 0 {
		fmt.Printf("\nfrozen counts over an open set (describe the mechanism, or link a generated list):\n")
		for _, h := range hits {
			fmt.Println("  " + h)
		}
		return false
	}
	fmt.Println("\nNO FROZEN COUNTS OVER OPEN SETS")
	return true
}
