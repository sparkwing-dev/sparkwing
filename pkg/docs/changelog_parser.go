package docs

import (
	"regexp"
	"strings"
)

// ReleaseSection records one changelog section and its byte and line positions.
// HeaderEnd excludes the heading's line ending. End is the next section or EOF.
type ReleaseSection struct {
	Version                          string
	StartLine, Start, HeaderEnd, End int
	Headings                         []ReleaseHeading
	Entries                          []ReleaseEntry
}

// ReleaseHeading locates a category heading without imposing lint policy.
type ReleaseHeading struct {
	Line int
	Name string
}

// ReleaseEntry retains a top-level bullet and its continuation lines.
type ReleaseEntry struct {
	TitleLine int
	Body      string
}

var (
	releaseHeadingRE  = regexp.MustCompile(`^##\s+(.+)$`)
	categoryHeadingRE = regexp.MustCompile(`^###\s+(.+)$`)
	releaseLabelRE    = regexp.MustCompile(`(?i)\[?(Unreleased|v\d+\.\d+\.\d+)\]?`)
)

// ParseChangelog locates release headings, categories and multiline entries.
// Brackets are optional, CRLF is accepted, and unknown labels remain available
// to consumers that validate their own release or category policy.
func ParseChangelog(body string) []ReleaseSection {
	var sections []ReleaseSection
	var current *ReleaseSection
	var entry *ReleaseEntry
	flush := func() {
		if current != nil && entry != nil {
			current.Entries = append(current.Entries, *entry)
			entry = nil
		}
	}
	offset, lineNumber := 0, 0
	for raw := range strings.SplitAfterSeq(body, "\n") {
		if raw == "" {
			continue
		}
		start := offset
		offset += len(raw)
		lineNumber++
		line := strings.TrimSuffix(strings.TrimSuffix(raw, "\n"), "\r")
		if match := releaseHeadingRE.FindStringSubmatch(line); match != nil {
			flush()
			if current != nil {
				current.End = start
				sections = append(sections, *current)
			}
			version := strings.TrimSpace(match[1])
			if label := releaseLabelRE.FindStringSubmatch(version); label != nil {
				version = label[1]
			}
			current = &ReleaseSection{Version: version, StartLine: lineNumber, Start: start, HeaderEnd: start + len(line), End: len(body)}
			continue
		}
		if current == nil {
			continue
		}
		if match := categoryHeadingRE.FindStringSubmatch(line); match != nil {
			flush()
			current.Headings = append(current.Headings, ReleaseHeading{Line: lineNumber, Name: strings.TrimSpace(match[1])})
		} else if strings.HasPrefix(line, "- ") || line == "-" {
			flush()
			entry = &ReleaseEntry{TitleLine: lineNumber, Body: line}
		} else if entry != nil {
			entry.Body += "\n" + line
		}
	}
	flush()
	if current != nil {
		sections = append(sections, *current)
	}
	return sections
}
