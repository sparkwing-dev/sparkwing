package main

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

// mdLinkRE matches a markdown link's target: [text](target).
var mdLinkRE = regexp.MustCompile(`\[[^\]]*\]\(([^)\s]+)\)`)

// checkLinks verifies that every intra-doc markdown link to a `.md`
// page resolves to a file that exists (relative to the linking file).
// This catches the rot where a page links to a renamed or deleted
// doc -- increasingly likely as concept pages link to the generated
// references. External (http/mailto) links and in-page anchors are
// left alone; only the file part of a `.md` target is checked.
func checkLinks(contentDir string) bool {
	var broken []string
	var checked int
	_ = filepath.Walk(contentDir, func(path string, info os.FileInfo, err error) error {
		if err != nil || info.IsDir() || !strings.HasSuffix(path, ".md") {
			return err
		}
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
