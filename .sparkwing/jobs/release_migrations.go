package jobs

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"unicode"
)

const (
	migrationsDirRel       = "docs/migrations"
	mirrorMigrationsDirRel = "pkg/docs/mirror/migrations"
	unreleasedGuideName    = "_unreleased.md"
	migrationIndexName     = "README.md"
)

// safety: the placeholder the checks read as "this release owes no guide
// yet"; a breaking entry links into it until the cut repoints the link.
const freshUnreleasedGuide = "# Migrating to the next release\n\nNo breaking changes so far.\n"

type migrationRoll struct {
	needed         bool
	guideName      string
	guideBody      string
	unreleasedBody string
	indexBody      string
	changelogBody  string
	summary        string
	breaking       int
	repointed      int
}

// safety: the roll writes the source docs and the embedded mirror together
// because the pre-commit tier judges the whole tree against the mirror, so a
// release commit that touched one alone could not be committed.
func migrationFileTargets(repoDir, name string) []string {
	return []string{
		filepath.Join(repoDir, filepath.FromSlash(migrationsDirRel), name),
		filepath.Join(repoDir, filepath.FromSlash(mirrorMigrationsDirRel), name),
	}
}

func migrationFileRels(name string) []string {
	return []string{
		migrationsDirRel + "/" + name,
		mirrorMigrationsDirRel + "/" + name,
	}
}

func releasedSection(body, version string) (changelogSection, bool) {
	for _, s := range parseChangelogSections(body) {
		if strings.EqualFold(s.version, version) {
			return s, true
		}
	}
	return changelogSection{}, false
}

func breakingEntries(s changelogSection) []changelogEntry {
	var out []changelogEntry
	for _, e := range s.entries {
		if breakingScopeRe.MatchString(e.body) {
			out = append(out, e)
		}
	}
	return out
}

func entryScope(e changelogEntry) string {
	if m := breakingScopeRe.FindStringSubmatch(e.body); m != nil {
		return strings.TrimSpace(m[1])
	}
	return "(Breaking)"
}

// safety: a section with no (Breaking) entry owes no guide, so the roll reports
// needed false rather than renaming the placeholder into a version nobody links.
func planMigrationRoll(changelogBody, guideSource, indexBody, version, date string) (migrationRoll, error) {
	sec, ok := releasedSection(changelogBody, version)
	if !ok {
		return migrationRoll{}, fmt.Errorf("CHANGELOG.md carries no [%s] section to roll the migration guide from", version)
	}
	breaking := breakingEntries(sec)
	if len(breaking) == 0 {
		return migrationRoll{}, nil
	}
	guideName := version + ".md"
	headings := markdownHeadings(guideSource)
	if err := refuseUnguidedBreaking(breaking, version, guideName, headings); err != nil {
		return migrationRoll{}, err
	}
	rolled, repointed := repointMigrationLinks(changelogBody, sec.startLine, guideName)
	summary := breakingSummary(breaking)
	index, err := insertMigrationIndexRow(indexBody, version, date, summary)
	if err != nil {
		return migrationRoll{}, err
	}
	return migrationRoll{
		needed:         true,
		guideName:      guideName,
		guideBody:      retitleGuide(guideSource, version),
		unreleasedBody: freshUnreleasedGuide,
		indexBody:      index,
		changelogBody:  rolled,
		summary:        summary,
		breaking:       len(breaking),
		repointed:      repointed,
	}, nil
}

// safety: the guide's prose is a person's work, so an entry with nothing to
// point at stops the cut instead of tagging a release whose (Breaking) entry
// resolves to nothing and whose merge back to main cannot be committed.
func refuseUnguidedBreaking(breaking []changelogEntry, version, guideName string, headings []string) error {
	var faults []string
	for _, e := range breaking {
		links := migrationLinkRe.FindAllStringSubmatch(e.body, -1)
		if len(links) == 0 {
			if pinnedToOwnRelease(version, e.body) {
				continue
			}
			faults = append(faults, fmt.Sprintf("CHANGELOG.md:%d: **%s (Breaking):** carries no docs/migrations/ link",
				e.titleLine, entryScope(e)))
			continue
		}
		for _, m := range links {
			path, anchor, _ := strings.Cut(m[1], "#")
			path = strings.TrimSpace(path)
			anchor = strings.TrimSpace(anchor)
			if path != unreleasedGuideName && path != guideName {
				faults = append(faults, fmt.Sprintf("CHANGELOG.md:%d: **%s (Breaking):** links to docs/migrations/%s, which this release does not roll",
					e.titleLine, entryScope(e), path))
				continue
			}
			if anchor == "" {
				faults = append(faults, fmt.Sprintf("CHANGELOG.md:%d: **%s (Breaking):** links to docs/migrations/%s with no #anchor; the guide carries %s",
					e.titleLine, entryScope(e), path, formatAnchorList(headings)))
				continue
			}
			if !anchorInHeadings(anchor, headings) {
				faults = append(faults, fmt.Sprintf("CHANGELOG.md:%d: **%s (Breaking):** links to #%s, which matches no heading in docs/migrations/%s; it carries %s",
					e.titleLine, entryScope(e), anchor, unreleasedGuideName, formatAnchorList(headings)))
			}
		}
	}
	if len(faults) == 0 {
		return nil
	}
	return fmt.Errorf("[%s] has %d (Breaking) entry link(s) the migration guide does not cover:\n%s\n"+
		"Write a section for each in %s/%s and link the entry to %s/%s#<anchor>; "+
		"the cut repoints those links to %s/%s. A guide section is written by a person, so the release stops here",
		version, len(faults), strings.Join(faults, "\n"),
		migrationsDirRel, unreleasedGuideName, migrationsDirRel, unreleasedGuideName, migrationsDirRel, guideName)
}

// safety: scoped to the section being cut so an older released section, which
// links its own guide, keeps the link it shipped with.
var unreleasedLinkRe = regexp.MustCompile(`\(docs/migrations/_unreleased\.md(#[^)]*)?\)`)

func repointMigrationLinks(body string, sectionStartLine int, guideName string) (string, int) {
	lines := strings.Split(body, "\n")
	repointed := 0
	for i := sectionStartLine; i < len(lines); i++ {
		if strings.HasPrefix(lines[i], "## ") {
			break
		}
		replaced := unreleasedLinkRe.ReplaceAllString(lines[i], "(docs/migrations/"+guideName+"$1)")
		if replaced != lines[i] {
			repointed += len(unreleasedLinkRe.FindAllString(lines[i], -1))
			lines[i] = replaced
		}
	}
	return strings.Join(lines, "\n"), repointed
}

var guideTitleRe = regexp.MustCompile(`(?m)^#\s+.*$`)

func retitleGuide(guideSource, version string) string {
	title := "# Migrating to " + version
	if loc := guideTitleRe.FindStringIndex(guideSource); loc != nil {
		return guideSource[:loc[0]] + title + guideSource[loc[1]:]
	}
	return title + "\n\n" + strings.TrimLeft(guideSource, "\n")
}

var migrationIndexSeparatorRe = regexp.MustCompile(`(?m)^\|[\s\-:|]+\|\s*$`)

func indexHasVersionRow(indexBody, version string) bool {
	return regexp.MustCompile(`(?m)^\|\s*\[` + regexp.QuoteMeta(version) + `\]`).MatchString(indexBody)
}

func insertMigrationIndexRow(indexBody, version, date, summary string) (string, error) {
	if indexHasVersionRow(indexBody, version) {
		return indexBody, nil
	}
	loc := migrationIndexSeparatorRe.FindStringIndex(indexBody)
	if loc == nil {
		return "", fmt.Errorf("%s/%s has no `|---|---|---|` table separator to add the %s row under",
			migrationsDirRel, migrationIndexName, version)
	}
	row := fmt.Sprintf("| [%s](%s.md) | %s | %s |", version, version, date, summary)
	return indexBody[:loc[1]] + "\n" + row + indexBody[loc[1]:], nil
}

// safety: the index summary has to describe this release, and _unreleased.md's
// title is a fixed placeholder, so the sentences the breaking entries already
// carry are the only per-release prose available at cut time.
func breakingSummary(breaking []changelogEntry) string {
	parts := make([]string, 0, len(breaking))
	for _, e := range breaking {
		s := firstSentence(flattenEntryBody(e.body))
		if s == "" {
			continue
		}
		parts = append(parts, strings.TrimSuffix(s, "."))
	}
	if len(parts) == 0 {
		return "See the guide."
	}
	return strings.Join(parts, "; ") + "."
}

var entryPrefixRe = regexp.MustCompile(`^-\s+\*\*[^*]+\*\*\s*`)

func flattenEntryBody(body string) string {
	flat := strings.Join(strings.Fields(body), " ")
	return strings.TrimSpace(entryPrefixRe.ReplaceAllString(flat, ""))
}

// safety: a changelog sentence carries version numbers, `.pem`-style code spans
// and "e.g.", so the cut is taken only at a period that a new sentence follows.
func firstSentence(s string) string {
	runes := []rune(s)
	inCode := false
	for i, r := range runes {
		if r == '`' {
			inCode = !inCode
			continue
		}
		if inCode || r != '.' {
			continue
		}
		if i > 0 && unicode.IsDigit(runes[i-1]) {
			continue
		}
		next := nextNonSpace(runes, i+1)
		if next < 0 {
			return strings.TrimSpace(string(runes[:i+1]))
		}
		if runes[i+1] != ' ' && runes[i+1] != '\t' {
			continue
		}
		if unicode.IsUpper(runes[next]) || runes[next] == '`' {
			return strings.TrimSpace(string(runes[:i+1]))
		}
	}
	return strings.TrimSpace(s)
}

func nextNonSpace(runes []rune, from int) int {
	for i := from; i < len(runes); i++ {
		if !unicode.IsSpace(runes[i]) {
			return i
		}
	}
	return -1
}

func migrationGuideExists(repoDir, guideName string) bool {
	_, err := os.Stat(filepath.Join(repoDir, filepath.FromSlash(migrationsDirRel), guideName))
	return err == nil
}

func readMigrationFile(repoDir, name string) (string, error) {
	body, err := os.ReadFile(filepath.Join(repoDir, filepath.FromSlash(migrationsDirRel), name))
	if err != nil {
		return "", fmt.Errorf("read %s/%s: %w", migrationsDirRel, name, err)
	}
	return string(body), nil
}

// safety: a guide already on disk is how a rerun after a failed push leaves the
// tree, so the roll reports it rather than overwriting a rolled guide.
func planMigrationRollIn(repoDir, changelogBody, version, date string) (roll migrationRoll, alreadyRolled bool, err error) {
	if migrationGuideExists(repoDir, version+".md") {
		return migrationRoll{}, true, nil
	}
	guide, err := readMigrationFile(repoDir, unreleasedGuideName)
	if err != nil {
		return migrationRoll{}, false, err
	}
	index, err := readMigrationFile(repoDir, migrationIndexName)
	if err != nil {
		return migrationRoll{}, false, err
	}
	roll, err = planMigrationRoll(changelogBody, guide, index, version, date)
	if err != nil {
		return migrationRoll{}, false, err
	}
	return roll, false, nil
}

func writeMigrationRoll(repoDir string, roll migrationRoll) ([]string, error) {
	files := []struct {
		name string
		body string
	}{
		{roll.guideName, roll.guideBody},
		{unreleasedGuideName, roll.unreleasedBody},
		{migrationIndexName, roll.indexBody},
	}
	var written []string
	for _, f := range files {
		for _, path := range migrationFileTargets(repoDir, f.name) {
			if err := os.WriteFile(path, []byte(f.body), 0o644); err != nil {
				return nil, fmt.Errorf("write %s: %w", path, err)
			}
		}
		written = append(written, migrationFileRels(f.name)...)
	}
	return written, nil
}

// safety: the date the index row carries is the one the changelog heading was
// rewritten with, so a rerun over an already-renamed section does not claim a
// second release date.
var releaseSectionDateRe = regexp.MustCompile(`(?m)^## \[?([^\]\s]+)\]?\s+-\s+(\d{4}-\d{2}-\d{2})\s*$`)

func releaseSectionDate(body, version string) string {
	for _, m := range releaseSectionDateRe.FindAllStringSubmatch(body, -1) {
		if strings.EqualFold(m[1], version) {
			return m[2]
		}
	}
	return ""
}
