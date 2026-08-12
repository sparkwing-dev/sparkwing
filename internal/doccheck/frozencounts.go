package main

import (
	"fmt"
	"io"
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

// isGeneratedDoc reports whether the doc at path is produced by a
// generator rather than hand-authored: generated docs (cli-reference
// and its per-group pages, config-reference) open with an HTML comment
// whose first line starts "<!-- GENERATED". Prose-style gates don't
// apply to them -- their wording comes from code (command descriptions,
// struct godoc). Detecting the marker instead of hardcoding names keeps
// the exemption correct as generators add or remove pages.
func isGeneratedDoc(path string) bool {
	f, err := os.Open(path)
	if err != nil {
		return false
	}
	defer f.Close()
	buf := make([]byte, len(generatedMarkerPrefix))
	n, _ := io.ReadFull(f, buf)
	return strings.HasPrefix(string(buf[:n]), generatedMarkerPrefix)
}

const generatedMarkerPrefix = "<!-- GENERATED"

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
		if isGeneratedDoc(path) {
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
