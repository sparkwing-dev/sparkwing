package jobs

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing/fstest"
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
	guideOnDisk    bool
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

// safety: a section with no (Breaking) entry owes no guide, so the roll reports
// needed false rather than renaming the placeholder into a version nobody links.
func planMigrationRoll(changelogBody, guideSource, indexBody, version, date string, guideOnDisk bool) (migrationRoll, error) {
	sec, ok := releasedSection(changelogBody, version)
	if !ok {
		return migrationRoll{}, fmt.Errorf("CHANGELOG.md carries no [%s] section to roll the migration guide from", version)
	}
	breaking := breakingEntries(sec)
	if len(breaking) == 0 {
		return migrationRoll{}, nil
	}
	guideName := version + ".md"
	guideBody := retitleGuide(guideSource, version)
	rolledBody, repointed := repointMigrationLinks(changelogBody, sec.startLine, guideName)
	rolledSec, ok := releasedSection(rolledBody, version)
	if !ok {
		return migrationRoll{}, fmt.Errorf("CHANGELOG.md lost its [%s] section while repointing its links", version)
	}
	if err := refuseUnrolledBreaking(rolledSec, guideName, guideBody); err != nil {
		return migrationRoll{}, err
	}
	summary := breakingSummary(breaking)
	index, err := insertMigrationIndexRow(indexBody, version, date, summary)
	if err != nil {
		return migrationRoll{}, err
	}
	return migrationRoll{
		needed:         true,
		guideOnDisk:    guideOnDisk,
		guideName:      guideName,
		guideBody:      guideBody,
		unreleasedBody: freshUnreleasedGuide,
		indexBody:      index,
		changelogBody:  rolledBody,
		summary:        summary,
		breaking:       len(breaking),
		repointed:      repointed,
	}, nil
}

// safety: one judge for what a released section owes its guide. The pre-commit
// tier refuses a commit on exactly these findings, so reading them from the same
// lint stops the cut from tagging a tree nobody can commit afterwards.
func refuseUnrolledBreaking(rolled changelogSection, guideName, guideBody string) error {
	issues := lintSectionBreakingEntries(rolled, fstest.MapFS{guideName: &fstest.MapFile{Data: []byte(guideBody)}})
	if len(issues) == 0 {
		return nil
	}
	var b strings.Builder
	for _, i := range issues {
		b.WriteString("  " + i.Format() + "\n")
	}
	return fmt.Errorf("%s does not cover every (Breaking) entry in [%s]:\n%s"+
		"Write a section for each in %s/%s and link the entry to its anchor; the cut repoints those links to %s/%s. "+
		"A guide section is written by a person, so the release stops here",
		migrationsDirRel+"/"+guideName, rolled.version, b.String(),
		migrationsDirRel, unreleasedGuideName, migrationsDirRel, guideName)
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

// safety: a changelog sentence carries `.pem`-style code spans, version numbers
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

func readMigrationFile(repoDir, name string) (string, error) {
	body, err := os.ReadFile(filepath.Join(repoDir, filepath.FromSlash(migrationsDirRel), name))
	if err != nil {
		return "", fmt.Errorf("read %s/%s: %w", migrationsDirRel, name, err)
	}
	return string(body), nil
}

// safety: a guide already on disk is a hand-roll or a rerun after a failed push,
// so its text becomes the authority and only its file write is skipped; the
// refusal, the link repoint and the index row still run or the cut ships the
// defect the guide was rolled to prevent.
func planMigrationRollIn(repoDir, changelogBody, version, date string) (migrationRoll, error) {
	guideSource, guideOnDisk, err := readGuideSource(repoDir, version+".md")
	if err != nil {
		return migrationRoll{}, err
	}
	index, err := readMigrationFile(repoDir, migrationIndexName)
	if err != nil {
		return migrationRoll{}, err
	}
	return planMigrationRoll(changelogBody, guideSource, index, version, date, guideOnDisk)
}

func readGuideSource(repoDir, guideName string) (body string, onDisk bool, err error) {
	if rolled, readErr := readMigrationFile(repoDir, guideName); readErr == nil {
		return rolled, true, nil
	}
	body, err = readMigrationFile(repoDir, unreleasedGuideName)
	return body, false, err
}

func writeMigrationRoll(repoDir string, roll migrationRoll) ([]string, error) {
	files := []struct {
		name string
		body string
		keep bool
	}{
		{roll.guideName, roll.guideBody, roll.guideOnDisk},
		{unreleasedGuideName, roll.unreleasedBody, false},
		{migrationIndexName, roll.indexBody, false},
	}
	var touched []string
	for _, f := range files {
		if !f.keep {
			for _, path := range migrationFileTargets(repoDir, f.name) {
				if err := os.WriteFile(path, []byte(f.body), 0o644); err != nil {
					return nil, fmt.Errorf("write %s: %w", path, err)
				}
			}
		}
		touched = append(touched, migrationFileRels(f.name)...)
	}
	return touched, nil
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
