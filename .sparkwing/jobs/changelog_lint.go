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

// ChangelogIssue is a single linter finding against CHANGELOG.md or
// one of the migration guides. Multiple issues are emitted independently
// so the author can fix them in one pass.
type ChangelogIssue struct {
	Line     int    // 1-based line in CHANGELOG.md
	Category string // duplicate-heading | missing-migration-link | missing-migration-file | missing-migration-anchor | version-mismatch
	Message  string
}

// Format renders an issue in the standard linter line format:
//
//	CHANGELOG.md:42: duplicate-heading: ### Added appears twice in [Unreleased] (first at line 12)
func (i ChangelogIssue) Format() string {
	return fmt.Sprintf("CHANGELOG.md:%d: %s: %s", i.Line, i.Category, i.Message)
}

// LintChangelog enforces the rubric in docs/changelog-style.md against
// the given CHANGELOG body, resolving migration-guide links against
// the supplied fs.FS rooted at docs/migrations/.
//
// Two classes of check:
//
//   - Dedupe: inside any `## [Unreleased]` or `## [vX.Y.Z]` section,
//     each `### <Category>` heading appears at most once. Unknown
//     category names are not flagged (a separate known-categories
//     check is out of scope).
//   - Breaking entries link to a migration guide: a bullet whose
//     first line matches `- **<scope> (Breaking):**` MUST contain
//     a markdown link of the form
//     `[anything](docs/migrations/v<X.Y.Z>.md#<anchor>)`, the file
//     must exist in migrations, the anchor must resolve to a `##`
//     heading in that file, and the version in the path must match
//     the section the entry lives in. Inside `[Unreleased]` the
//     link can be missing entirely or point at
//     `docs/migrations/_unreleased.md` -- the release-time agent
//     fills it in at tag time.
//
// Pure function: no filesystem reads, no globals. The caller wires
// the os.DirFS in CheckChangelogLint.
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

// CheckChangelogLint is the side-effecting entry point used by the
// lint pipeline. Reads CHANGELOG.md + docs/migrations/ from repoRoot
// and returns an aggregated error listing every issue.
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
		// No migrations dir yet -- pass an empty FS so file-existence
		// checks all fail cleanly.
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

// schemaBreakCategory is the issue category emitted when the runs-store
// schema version changed between releases but the changelog never marked
// the change `(Breaking)`.
const schemaBreakCategory = "unmarked-schema-break"

// LintSchemaBreak fails an unmarked runs-store schema change. When the
// embedded schema version differs between the previous release
// (prevSchema) and the release being cut (curSchema), the changelog must
// record the break: the section for the version being cut -- or
// `[Unreleased]` before the release rewrite renames it -- must carry a
// `- **<scope> (Breaking):**` entry that names the schema. An unchanged
// schema yields no issue.
//
// Pure function: the caller sources prevSchema/curSchema from the store
// source at each git ref. A schema bump without a marked breaking entry
// is the failure mode the releases standard calls out (point 2) and the
// reason an unmarked break slipped a release.
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

// sectionHasSchemaBreakEntry reports whether the section carries a
// top-level `(Breaking)` bullet that names the schema (case-insensitive
// match on "schema" anywhere in the entry, title or continuation).
func sectionHasSchemaBreakEntry(s changelogSection) bool {
	for _, e := range s.entries {
		if breakingScopeRe.MatchString(e.body) && strings.Contains(strings.ToLower(e.body), "schema") {
			return true
		}
	}
	return false
}

// changelogSection is one `## [...]` block: the version label, the
// line range it covers, and the entries inside.
type changelogSection struct {
	version   string            // "Unreleased" or "v0.4.0" (no brackets, no date)
	startLine int               // 1-based line of the `## [...]` heading
	subHeads  []subheadingMatch // every `### X` line under this section
	entries   []changelogEntry  // top-level `- ...` bullets
}

type subheadingMatch struct {
	line int    // 1-based
	name string // raw heading text after "### "
}

// changelogEntry is one top-level `- ...` bullet plus its continuation
// lines (everything indented or blank under it until the next bullet
// or sub-heading).
type changelogEntry struct {
	titleLine int    // 1-based line of the `-` bullet
	body      string // joined entry text (title + continuations)
}

var (
	sectionHeadingRe    = regexp.MustCompile(`^##\s+(.+)$`)
	subHeadingRe        = regexp.MustCompile(`^###\s+(.+)$`)
	breakingScopeRe     = regexp.MustCompile(`(?m)^-\s+\*\*([^*]+?)\s*\(Breaking\)\s*:\*\*`)
	migrationLinkRe     = regexp.MustCompile(`\(docs/migrations/([^)]+)\)`)
	sectionVersionLabel = regexp.MustCompile(`\[?(Unreleased|v\d+\.\d+\.\d+)\]?`)
)

// parseChangelogSections walks the body line by line and groups every
// `## [Unreleased]` / `## [vX.Y.Z]` heading with the sub-headings and
// top-level bullets that follow it.
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
		// Top-level bullet: starts at column 0 with "- ".
		if strings.HasPrefix(line, "- ") {
			flushEntry()
			entry = &changelogEntry{titleLine: lineNum, body: line}
			continue
		}
		// Continuation of the current entry: blank line, or indented
		// content. Append until the next bullet/heading.
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

// lintSectionHeadingsDedupe emits a duplicate-heading issue for each
// sub-heading that appears more than once in the section. The issue's
// Line is the second (or later) occurrence so the author can merge
// upward.
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

// normalizeHeadingName is the equality used for dedupe: case-sensitive
// matching of the heading text after `### `. `### Added` and `### added`
// are treated as distinct so authors who deliberately use lowercase
// don't get falsely deduped -- the related case-correction check is
// a separate concern.
func normalizeHeadingName(s string) string { return s }

// lintSectionBreakingEntries scans every top-level bullet for the
// `**<scope> (Breaking):**` shape, then validates each one's migration
// link.
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

// validateMigrationLink resolves one `docs/migrations/...` URL against
// the migrations FS and the containing section.
func validateMigrationLink(s changelogSection, e changelogEntry, urlTail string, migrations fs.FS, isUnreleased bool) []ChangelogIssue {
	// Split path from anchor.
	path, anchor, _ := strings.Cut(urlTail, "#")
	path = strings.TrimSpace(path)
	anchor = strings.TrimSpace(anchor)

	// Placeholder for [Unreleased]: the release agent fills these in.
	if isUnreleased && path == "_unreleased.md" {
		return nil
	}

	// Version-mismatch check: only meaningful inside a versioned section.
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

	// File-existence check.
	headings, fileExists := readMigrationHeadings(migrations, path)
	if !fileExists {
		return []ChangelogIssue{{
			Line:     e.titleLine,
			Category: "missing-migration-file",
			Message: fmt.Sprintf("(Breaking) entry links to docs/migrations/%s but the file does not exist (create it with the H2 sections the entry references)",
				path),
		}}
	}

	// Anchor-resolution check.
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

// readMigrationHeadings returns the slugifiable text of every `## `
// heading in the migration file, plus a flag indicating whether the
// file exists at all.
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

// slugifyHeading mirrors GitHub's heading-to-anchor algorithm closely
// enough for the cases we care about: lowercase the heading, drop any
// character that isn't a letter, digit, hyphen, underscore, or space,
// then convert spaces to hyphens.
//
// Edge cases checked in tests; complex inline code or emoji headings
// may differ slightly from GitHub's exact behavior. Authors hitting
// that should rename the heading to a simpler shape.
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
			// Drop everything else (parens, periods, backticks,
			// arrows, em-dashes, etc.).
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

// emptyFS is the fs.FS used when docs/migrations/ doesn't exist yet.
// Every Open returns ErrNotExist, which makes the file-existence check
// produce a missing-migration-file issue rather than panicking.
type emptyFS struct{}

func (emptyFS) Open(string) (fs.File, error) { return nil, fs.ErrNotExist }
