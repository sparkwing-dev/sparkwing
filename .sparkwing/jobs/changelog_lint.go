package jobs

import (
	"bufio"
	"context"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
)

type ChangelogIssue struct {
	Line     int
	Category string
	Message  string
}

func (i ChangelogIssue) Format() string {
	return fmt.Sprintf("CHANGELOG.md:%d: %s: %s", i.Line, i.Category, i.Message)
}

func LintChangelog(body string, migrations fs.FS) []ChangelogIssue {
	var issues []ChangelogIssue
	sections := parseChangelogSections(body)
	for _, s := range sections {
		issues = append(issues, lintSectionHeadingsDedupe(s)...)
		issues = append(issues, lintSectionBreakingEntries(s, migrations)...)
	}
	sortIssues(issues)
	return issues
}

func CheckChangelogLint(ctx context.Context, repoRoot string) error {
	body, err := os.ReadFile(filepath.Join(repoRoot, "CHANGELOG.md"))
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return fmt.Errorf("read CHANGELOG.md: %w", err)
	}
	migrationsDir := filepath.Join(repoRoot, "docs", "migrations")
	var migrations fs.FS
	if info, statErr := os.Stat(migrationsDir); statErr == nil && info.IsDir() {
		migrations = os.DirFS(migrationsDir)
	} else {
		migrations = emptyFS{}
	}
	issues := LintChangelog(string(body), migrations)
	if len(issues) == 0 {
		return nil
	}
	var b strings.Builder
	for _, i := range issues {
		b.WriteString(i.Format())
		b.WriteByte('\n')
	}
	fmt.Fprintf(&b, "%d issue(s)\n", len(issues))
	return fmt.Errorf("%s", b.String())
}

const schemaBreakCategory = "unmarked-schema-break"

func LintSchemaBreak(body, version string, prevSchema, curSchema int) []ChangelogIssue {
	if prevSchema == curSchema {
		return nil
	}
	sections := parseChangelogSections(body)
	var versionSec, unreleasedSec *changelogSection
	for i := range sections {
		switch {
		case strings.EqualFold(sections[i].version, version):
			versionSec = &sections[i]
		case strings.EqualFold(sections[i].version, "Unreleased"):
			unreleasedSec = &sections[i]
		}
	}
	for _, s := range []*changelogSection{versionSec, unreleasedSec} {
		if s != nil && sectionHasSchemaBreakEntry(*s) {
			return nil
		}
	}
	line := 1
	label := version
	switch {
	case versionSec != nil:
		line = versionSec.startLine
		label = versionSec.version
	case unreleasedSec != nil:
		line = unreleasedSec.startLine
		label = unreleasedSec.version
	}
	return []ChangelogIssue{{
		Line:     line,
		Category: schemaBreakCategory,
		Message: fmt.Sprintf(
			"runs-store schema changed %d -> %d but [%s] has no `(Breaking)` entry naming the schema; mark the change `(Breaking)` and ship a docs/migrations/%s.md schema section",
			prevSchema, curSchema, label, version),
	}}
}

func sectionHasSchemaBreakEntry(s changelogSection) bool {
	for _, e := range s.entries {
		if breakingScopeRe.MatchString(e.body) && strings.Contains(strings.ToLower(e.body), "schema") {
			return true
		}
	}
	return false
}

type changelogSection struct {
	version   string
	startLine int
	subHeads  []subheadingMatch
	entries   []changelogEntry
}

type subheadingMatch struct {
	line int
	name string
}

type changelogEntry struct {
	titleLine int
	body      string
}

var (
	sectionHeadingRe    = regexp.MustCompile(`^##\s+(.+)$`)
	subHeadingRe        = regexp.MustCompile(`^###\s+(.+)$`)
	breakingScopeRe     = regexp.MustCompile(`(?m)^-\s+\*\*([^*]+?)\s*\(Breaking\)\s*:\*\*`)
	migrationLinkRe     = regexp.MustCompile(`\(docs/migrations/([^)]+)\)`)
	sectionVersionLabel = regexp.MustCompile(`\[?(Unreleased|v\d+\.\d+\.\d+)\]?`)
)

func parseChangelogSections(body string) []changelogSection {
	var sections []changelogSection
	var cur *changelogSection
	var entry *changelogEntry
	scanner := bufio.NewScanner(strings.NewReader(body))
	scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	lineNum := 0
	flushEntry := func() {
		if cur == nil || entry == nil {
			return
		}
		cur.entries = append(cur.entries, *entry)
		entry = nil
	}
	for scanner.Scan() {
		lineNum++
		line := scanner.Text()
		if m := sectionHeadingRe.FindStringSubmatch(line); m != nil {
			flushEntry()
			if cur != nil {
				sections = append(sections, *cur)
			}
			label := sectionVersionLabel.FindStringSubmatch(m[1])
			version := ""
			if label != nil {
				version = label[1]
			} else {
				version = strings.TrimSpace(m[1])
			}
			cur = &changelogSection{version: version, startLine: lineNum}
			continue
		}
		if cur == nil {
			continue
		}
		if m := subHeadingRe.FindStringSubmatch(line); m != nil {
			flushEntry()
			cur.subHeads = append(cur.subHeads, subheadingMatch{
				line: lineNum,
				name: strings.TrimSpace(m[1]),
			})
			continue
		}
		if strings.HasPrefix(line, "- ") {
			flushEntry()
			entry = &changelogEntry{titleLine: lineNum, body: line}
			continue
		}
		if entry != nil {
			entry.body += "\n" + line
		}
	}
	flushEntry()
	if cur != nil {
		sections = append(sections, *cur)
	}
	return sections
}

func lintSectionHeadingsDedupe(s changelogSection) []ChangelogIssue {
	var issues []ChangelogIssue
	firstSeen := map[string]int{}
	for _, h := range s.subHeads {
		key := normalizeHeadingName(h.name)
		if prev, ok := firstSeen[key]; ok {
			issues = append(issues, ChangelogIssue{
				Line:     h.line,
				Category: "duplicate-heading",
				Message: fmt.Sprintf("### %s appears more than once in [%s] (first at line %d); merge the entries into one block",
					h.name, s.version, prev),
			})
			continue
		}
		firstSeen[key] = h.line
	}
	return issues
}

func normalizeHeadingName(s string) string { return s }

func lintSectionBreakingEntries(s changelogSection, migrations fs.FS) []ChangelogIssue {
	var issues []ChangelogIssue
	isUnreleased := strings.EqualFold(s.version, "Unreleased")
	for _, e := range s.entries {
		if !breakingScopeRe.MatchString(e.body) {
			continue
		}
		linkMatches := migrationLinkRe.FindAllStringSubmatch(e.body, -1)
		if len(linkMatches) == 0 {
			if isUnreleased {
				continue
			}
			issues = append(issues, ChangelogIssue{
				Line:     e.titleLine,
				Category: "missing-migration-link",
				Message: fmt.Sprintf("(Breaking) entry in [%s] is missing a `docs/migrations/v%s.md#<anchor>` link",
					s.version, strings.TrimPrefix(s.version, "v")),
			})
			continue
		}
		for _, m := range linkMatches {
			issues = append(issues, validateMigrationLink(s, e, m[1], migrations, isUnreleased)...)
		}
	}
	return issues
}

func validateMigrationLink(s changelogSection, e changelogEntry, urlTail string, migrations fs.FS, isUnreleased bool) []ChangelogIssue {
	path, anchor, _ := strings.Cut(urlTail, "#")
	path = strings.TrimSpace(path)
	anchor = strings.TrimSpace(anchor)

	if isUnreleased && path == "_unreleased.md" {
		return nil
	}

	if !isUnreleased {
		expected := s.version
		if !strings.HasPrefix(expected, "v") {
			expected = "v" + expected
		}
		expectedPath := expected + ".md"
		if path != expectedPath {
			return []ChangelogIssue{{
				Line:     e.titleLine,
				Category: "version-mismatch",
				Message: fmt.Sprintf("(Breaking) entry in [%s] links to docs/migrations/%s but should link to docs/migrations/%s",
					s.version, path, expectedPath),
			}}
		}
	}

	headings, fileExists := readMigrationHeadings(migrations, path)
	if !fileExists {
		return []ChangelogIssue{{
			Line:     e.titleLine,
			Category: "missing-migration-file",
			Message: fmt.Sprintf("(Breaking) entry links to docs/migrations/%s but the file does not exist (create it with the H2 sections the entry references)",
				path),
		}}
	}

	if anchor == "" {
		return []ChangelogIssue{{
			Line:     e.titleLine,
			Category: "missing-migration-anchor",
			Message: fmt.Sprintf("(Breaking) entry links to docs/migrations/%s but the link has no #anchor; available headings: %s",
				path, formatAnchorList(headings)),
		}}
	}
	for _, h := range headings {
		if slugifyHeading(h) == anchor {
			return nil
		}
	}
	return []ChangelogIssue{{
		Line:     e.titleLine,
		Category: "missing-migration-anchor",
		Message: fmt.Sprintf("(Breaking) entry links to docs/migrations/%s#%s but that anchor does not match any H2 in the file; available headings: %s",
			path, anchor, formatAnchorList(headings)),
	}}
}

func readMigrationHeadings(migrations fs.FS, path string) ([]string, bool) {
	if migrations == nil {
		return nil, false
	}
	f, err := migrations.Open(path)
	if err != nil {
		return nil, false
	}
	defer func() { _ = f.Close() }()
	var headings []string
	scanner := bufio.NewScanner(f)
	scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	for scanner.Scan() {
		line := scanner.Text()
		if strings.HasPrefix(line, "## ") {
			headings = append(headings, strings.TrimSpace(strings.TrimPrefix(line, "## ")))
		}
	}
	return headings, true
}

func slugifyHeading(s string) string {
	var b strings.Builder
	for _, r := range strings.ToLower(s) {
		switch {
		case r >= 'a' && r <= 'z':
			b.WriteRune(r)
		case r >= '0' && r <= '9':
			b.WriteRune(r)
		case r == '-' || r == '_':
			b.WriteRune(r)
		case r == ' ' || r == '\t':
			b.WriteRune('-')
		default:
		}
	}
	return b.String()
}

func formatAnchorList(headings []string) string {
	if len(headings) == 0 {
		return "(file has no H2 headings yet)"
	}
	anchors := make([]string, 0, len(headings))
	for _, h := range headings {
		anchors = append(anchors, "#"+slugifyHeading(h))
	}
	return strings.Join(anchors, ", ")
}

func sortIssues(issues []ChangelogIssue) {
	sort.SliceStable(issues, func(i, j int) bool {
		if issues[i].Line != issues[j].Line {
			return issues[i].Line < issues[j].Line
		}
		return issues[i].Category < issues[j].Category
	})
}

type emptyFS struct{}

func (emptyFS) Open(string) (fs.File, error) { return nil, fs.ErrNotExist }
