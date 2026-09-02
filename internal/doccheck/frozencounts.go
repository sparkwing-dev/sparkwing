package main

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

var frozenCountRE = regexp.MustCompile(`(?i)\b(?:one|two|three|four|five|six|seven|eight|nine|ten|[0-9]+)[ -](places|ways|kinds|sources|triggers|checks|reasons|steps|stages|backends)\b`)

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

func checkFrozenCounts(contentDir string) bool {
	var hits []string
	// #nosec G703 -- a build-time tool reading paths the operator names
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
		// #nosec G122 -- the walk stays inside the repository this check runs in
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
