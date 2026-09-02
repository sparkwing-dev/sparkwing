package main

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

var mdLinkRE = regexp.MustCompile(`\[[^\]]*\]\(([^)\s]+)\)`)

func checkLinks(contentDir string) bool {
	var broken []string
	var checked int
	// #nosec G703 -- a build-time tool reading paths the operator names
	_ = filepath.Walk(contentDir, func(path string, info os.FileInfo, err error) error {
		if err != nil || info.IsDir() || !strings.HasSuffix(path, ".md") {
			return err
		}
		// #nosec G122 -- a TOCTOU swap here needs write access to the checkout this build-time check already trusts
		data, rerr := os.ReadFile(path)
		if rerr != nil {
			return rerr
		}
		rel, _ := filepath.Rel(contentDir, path)
		for ln, line := range strings.Split(string(data), "\n") {
			for _, m := range mdLinkRE.FindAllStringSubmatch(line, -1) {
				target := m[1]
				if strings.HasPrefix(target, "http://") || strings.HasPrefix(target, "https://") ||
					strings.HasPrefix(target, "mailto:") || strings.HasPrefix(target, "#") {
					continue
				}
				file := target
				if i := strings.IndexByte(file, '#'); i >= 0 {
					file = file[:i]
				}
				if file == "" || !strings.HasSuffix(file, ".md") {
					continue
				}
				checked++
				resolved := filepath.Join(filepath.Dir(path), file)
				// #nosec G703 -- a build-time tool reading paths the operator names
				if _, statErr := os.Stat(resolved); statErr != nil {
					broken = append(broken, fmt.Sprintf("%s:%d: %q -> missing %s", rel, ln+1, target, file))
				}
			}
		}
		return nil
	})

	fmt.Printf("doccheck/links: %d intra-doc .md link(s) -- %d broken\n", checked, len(broken))
	if len(broken) > 0 {
		fmt.Printf("\n%d broken doc link(s) (renamed/deleted target):\n", len(broken))
		for _, b := range broken {
			fmt.Println("  " + b)
		}
		return false
	}
	fmt.Println("\nNO BROKEN DOC LINKS")
	return true
}
