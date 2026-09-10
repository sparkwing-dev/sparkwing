package jobs

import (
	"bufio"
	"fmt"
	"io/fs"
	"regexp"
	"sort"
	"strings"
)

const deadDocLinkCategory = "dead-docs-link"

// docLinkRe matches a Markdown link whose target is a repository-relative
// documentation path. CHANGELOG.md sits at the repository root, so those
// targets start at `docs/`.
var docLinkRe = regexp.MustCompile(`\]\((docs/[^)\s]+)\)`)

var headingRe = regexp.MustCompile(`^#{1,6}\s+(.+)$`)

// LintChangelogDocLinks reports every repository-relative documentation link in
// the changelog whose file is gone or whose anchor matches no heading in it.
// Links under docs/migrations/ are excluded: the breaking-entry check owns
// those and reports them against the release policy rather than as dead links.
func LintChangelogDocLinks(body string, repo fs.FS) []ChangelogIssue {
	var issues []ChangelogIssue
	headings := map[string][]string{}
	missing := map[string]bool{}

	scanner := bufio.NewScanner(strings.NewReader(body))
	scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	for line := 1; scanner.Scan(); line++ {
		for _, m := range docLinkRe.FindAllStringSubmatch(scanner.Text(), -1) {
			path, anchor, _ := strings.Cut(m[1], "#")
			if strings.HasPrefix(path, "docs/migrations/") {
				continue
			}
			if _, seen := headings[path]; !seen && !missing[path] {
				found, exists := readDocHeadings(repo, path)
				if exists {
					headings[path] = found
				} else {
					missing[path] = true
				}
			}
			if missing[path] {
				issues = append(issues, ChangelogIssue{
					Line:     line,
					Category: deadDocLinkCategory,
					Message:  fmt.Sprintf("links to %s but the file does not exist", path),
				})
				continue
			}
			if anchor == "" {
				continue
			}
			if !anchorInHeadings(anchor, headings[path]) {
				issues = append(issues, ChangelogIssue{
					Line:     line,
					Category: deadDocLinkCategory,
					Message: fmt.Sprintf("links to %s#%s but that anchor matches no heading in the file; available headings: %s",
						path, anchor, formatAnchorList(headings[path])),
				})
			}
		}
	}
	sortIssues(issues)
	return issues
}

func anchorInHeadings(anchor string, headings []string) bool {
	for _, h := range headings {
		if slugifyHeading(h) == anchor {
			return true
		}
	}
	return false
}

func readDocHeadings(repo fs.FS, path string) ([]string, bool) {
	if repo == nil {
		return nil, false
	}
	body, err := fs.ReadFile(repo, path)
	if err != nil {
		return nil, false
	}
	var headings []string
	for line := range strings.Lines(string(body)) {
		if m := headingRe.FindStringSubmatch(strings.TrimRight(line, "\r\n")); m != nil {
			headings = append(headings, strings.TrimSpace(m[1]))
		}
	}
	return headings, true
}

func mergeIssues(sets ...[]ChangelogIssue) []ChangelogIssue {
	var all []ChangelogIssue
	for _, set := range sets {
		all = append(all, set...)
	}
	sort.SliceStable(all, func(i, j int) bool { return all[i].Line < all[j].Line })
	return all
}
