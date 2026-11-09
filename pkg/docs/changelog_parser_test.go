package docs

import (
	"strings"
	"testing"
	"testing/fstest"
)

func TestChangelogParserSharesReleaseSpansWithCollection(t *testing.T) {
	for _, heading := range []string{"## [Unreleased]", "## Unreleased", "##\t[Unreleased]"} {
		for _, newline := range []string{"\n", "\r\n"} {
			body := strings.Join([]string{"# Changelog", heading, "", "## [v0.1.0] - 2026-01-01", "### Historical", "- old", ""}, newline)
			collected, err := CollectChangelog(body, fstest.MapFS{"changes/note.md": {Data: []byte("### Fixed\n\n- new\n  continuation\n")}})
			if err != nil {
				t.Fatal(err)
			}
			sections := ParseChangelog(collected)
			if len(sections) != 2 || len(sections[0].Entries) != 1 || len(sections[1].Entries) != 1 {
				t.Fatalf("sections = %+v", sections)
			}
			for _, section := range sections {
				text := collected[section.Start:section.End]
				if !strings.HasPrefix(text, "##") || !strings.Contains(text, section.Entries[0].Body) {
					t.Fatalf("invalid section spans: %+v", section)
				}
			}
			if !strings.Contains(sections[0].Entries[0].Body, "  continuation") || sections[1].Headings[0].Name != "Historical" {
				t.Fatalf("lost multiline entry or unknown category: %+v", sections)
			}
			if sections[0].StartLine != 2 {
				t.Fatalf("first heading line = %d", sections[0].StartLine)
			}
		}
	}
}
