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
	issues := LintChangelog(string(body), migrationsFS(repoRoot))
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

const (
	schemaBreakCategory  = "unmarked-schema-break"
	schemaChangeCategory = "unlogged-schema-change"
)

// LintSchemaBreak checks that a runs-store schema change is described in the
// changelog section being cut. addedRequirements names the schema requirements
// this release introduces: adding one strands every older binary and needs a
// `(Breaking)` entry plus a migration guide, while a purely additive bump keeps
// older binaries reading and writing and needs only a plain `**store:**` entry.
// A release that adds a requirement is checked even when the schema number
// does not move, because reclassifying an already-released migration strands
// binaries just as thoroughly as a bump does.
func LintSchemaBreak(body, version string, prevSchema, curSchema int, addedRequirements []string) []ChangelogIssue {
	if prevSchema == curSchema && len(addedRequirements) == 0 {
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
	breaking := len(addedRequirements) > 0
	satisfied := sectionHasStoreEntry
	if breaking {
		satisfied = sectionHasSchemaBreakEntry
	}
	for _, s := range []*changelogSection{versionSec, unreleasedSec} {
		if s != nil && satisfied(*s) {
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
	if breaking {
		return []ChangelogIssue{{
			Line:     line,
			Category: schemaBreakCategory,
			Message: fmt.Sprintf(
				"%s and adds requirement(s) %s, so a binary without them refuses the database, but [%s] has no `(Breaking)` entry naming the schema; mark the change `(Breaking)` and ship a docs/migrations/%s.md schema section",
				describeSchemaDelta(prevSchema, curSchema), strings.Join(addedRequirements, ", "), label, version),
		}}
	}
	return []ChangelogIssue{{
		Line:     line,
		Category: schemaChangeCategory,
		Message: fmt.Sprintf(
			"%s and adds no requirement, so older binaries keep opening the database, but [%s] has no `**store:**` entry describing it; add one",
			describeSchemaDelta(prevSchema, curSchema), label),
	}}
}

const (
	wireBreakCategory     = "unmarked-wire-break"
	wireMigrationCategory = "missing-wire-migration"
	wireCoverageCategory  = "undeclared-wire-cut"
)

// safety: the scope is what ties an entry to this surface. Prose keywords do
// not: "api" and "protocol" are ordinary words in entries about other things,
// and 16 of this repo's 85 released breaking entries carry one.
var wireScopes = []string{"wingd", "wingwire", "api"}

var wireScopeRe = regexp.MustCompile(`(?i)\b(wingd|wingwire|api)\b`)

// LintWireBreak checks that every cut in the daemon's wire surface is declared
// in the changelog section being cut. A cut needs a `(Breaking)` entry under a
// wire scope, a migration section its link resolves to, and its identifier
// spelled verbatim in one of the two, because a pinned pipeline binary meets
// the cut at run time and its operator has only the release notes to go on.
func LintWireBreak(body, version string, cuts []wireCut, migrations fs.FS) []ChangelogIssue {
	if len(cuts) == 0 {
		return nil
	}
	section := changelogSectionFor(body, version)
	if section == nil {
		return []ChangelogIssue{{
			Line:     1,
			Category: wireBreakCategory,
			Message: fmt.Sprintf("%s, but the changelog has no [%s] or [Unreleased] section to declare it in",
				describeWireCuts(cuts), version),
		}}
	}
	isUnreleased := strings.EqualFold(section.version, "Unreleased")
	var declarations []changelogEntry
	for _, e := range section.entries {
		if scope := breakingScopeRe.FindStringSubmatch(e.body); scope != nil && wireScopeRe.MatchString(scope[1]) {
			declarations = append(declarations, e)
		}
	}
	if len(declarations) == 0 {
		return []ChangelogIssue{{
			Line:     section.startLine,
			Category: wireBreakCategory,
			Message: fmt.Sprintf("%s, but [%s] has no `(Breaking)` entry under a wire scope (%s); mark the cut and link a section in docs/migrations/%s",
				describeWireCuts(cuts), section.version, strings.Join(wireScopes, ", "), expectedMigrationPath(section.version, isUnreleased)),
		}}
	}
	declared := ""
	var issues []ChangelogIssue
	for _, e := range declarations {
		links := migrationLinkRe.FindAllStringSubmatch(e.body, -1)
		if len(links) == 0 {
			issues = append(issues, ChangelogIssue{
				Line:     e.titleLine,
				Category: wireMigrationCategory,
				Message: fmt.Sprintf("`(Breaking)` entry in [%s] links no migration section; link docs/migrations/%s",
					section.version, expectedMigrationPath(section.version, isUnreleased)),
			})
			continue
		}
		sections, linkIssues := migrationSections(*section, e, links, migrations, isUnreleased)
		issues = append(issues, linkIssues...)
		if len(linkIssues) > 0 {
			continue
		}
		declared += e.body + "\n" + strings.Join(sections, "\n") + "\n"
	}
	if declared == "" {
		return issues
	}
	var missing []string
	for _, c := range cuts {
		if !strings.Contains(declared, c.Identifier) {
			missing = append(missing, c.describe())
		}
	}
	if len(missing) == 0 {
		return nil
	}
	return append(issues, ChangelogIssue{
		Line:     declarations[0].titleLine,
		Category: wireCoverageCategory,
		Message: fmt.Sprintf("the `(Breaking)` entry in [%s] and its migration section do not name %d of %d cut(s): %s; name each one verbatim in the entry or the section it links",
			section.version, len(missing), len(cuts), strings.Join(missing, "; ")),
	})
}

func migrationSections(s changelogSection, e changelogEntry, links [][]string, migrations fs.FS, isUnreleased bool) (bodies []string, issues []ChangelogIssue) {
	for _, link := range links {
		path, anchor, _ := strings.Cut(link[1], "#")
		path, anchor = strings.TrimSpace(path), strings.TrimSpace(anchor)
		if want := expectedMigrationPath(s.version, isUnreleased); path != want {
			issues = append(issues, ChangelogIssue{
				Line:     e.titleLine,
				Category: "version-mismatch",
				Message:  fmt.Sprintf("(Breaking) entry in [%s] links to docs/migrations/%s but should link to docs/migrations/%s", s.version, path, want),
			})
			continue
		}
		headings, exists := readMigrationHeadings(migrations, path)
		if !exists {
			issues = append(issues, ChangelogIssue{
				Line:     e.titleLine,
				Category: "missing-migration-file",
				Message:  fmt.Sprintf("(Breaking) entry links to docs/migrations/%s but the file does not exist", path),
			})
			continue
		}
		body, found := migrationSectionBody(migrations, path, anchor)
		if !found {
			issues = append(issues, ChangelogIssue{
				Line:     e.titleLine,
				Category: "missing-migration-anchor",
				Message: fmt.Sprintf("(Breaking) entry links to docs/migrations/%s#%s but that anchor matches no H2 in the file; available headings: %s",
					path, anchor, formatAnchorList(headings)),
			})
			continue
		}
		bodies = append(bodies, body)
	}
	return bodies, issues
}

func migrationSectionBody(migrations fs.FS, path, anchor string) (body string, found bool) {
	if anchor == "" || migrations == nil {
		return "", false
	}
	f, err := migrations.Open(path)
	if err != nil {
		return "", false
	}
	defer func() { _ = f.Close() }()
	var b strings.Builder
	scanner := bufio.NewScanner(f)
	scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	for scanner.Scan() {
		line := scanner.Text()
		if strings.HasPrefix(line, "## ") {
			if found {
				break
			}
			found = slugifyHeading(strings.TrimSpace(strings.TrimPrefix(line, "## "))) == anchor
			continue
		}
		if found {
			b.WriteString(line)
			b.WriteByte('\n')
		}
	}
	return b.String(), found
}

func expectedMigrationPath(version string, isUnreleased bool) string {
	if isUnreleased {
		return "_unreleased.md"
	}
	if !strings.HasPrefix(version, "v") {
		version = "v" + version
	}
	return version + ".md"
}

func changelogSectionFor(body, version string) *changelogSection {
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
	if versionSec != nil {
		return versionSec
	}
	return unreleasedSec
}

func describeWireCuts(cuts []wireCut) string {
	described := describeCuts(cuts)
	if len(described) == 1 {
		return "the daemon's wire surface cuts " + described[0]
	}
	if len(described) > 6 {
		return fmt.Sprintf("the daemon's wire surface cuts %d entries (%s, and %d more)",
			len(described), strings.Join(described[:6], "; "), len(described)-6)
	}
	return fmt.Sprintf("the daemon's wire surface cuts %d entries (%s)", len(described), strings.Join(described, "; "))
}

func describeSchemaDelta(prevSchema, curSchema int) string {
	if prevSchema == curSchema {
		return fmt.Sprintf("runs-store schema stays at %d", curSchema)
	}
	return fmt.Sprintf("runs-store schema changed %d -> %d", prevSchema, curSchema)
}

func sectionHasSchemaBreakEntry(s changelogSection) bool {
	for _, e := range s.entries {
		if breakingScopeRe.MatchString(e.body) && strings.Contains(strings.ToLower(e.body), "schema") {
			return true
		}
	}
	return false
}

func sectionHasStoreEntry(s changelogSection) bool {
	for _, e := range s.entries {
		if storeScopeRe.MatchString(e.body) {
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
	storeScopeRe        = regexp.MustCompile(`(?m)^-\s+\*\*[^*]*store[^*]*:\*\*`)
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

func migrationsFS(repoRoot string) fs.FS {
	dir := filepath.Join(repoRoot, "docs", "migrations")
	if info, err := os.Stat(dir); err == nil && info.IsDir() {
		return os.DirFS(dir)
	}
	return emptyFS{}
}

type emptyFS struct{}

func (emptyFS) Open(string) (fs.File, error) { return nil, fs.ErrNotExist }
