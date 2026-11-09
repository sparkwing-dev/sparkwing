package jobs

import (
	"strings"
	"testing"
)

func TestReleaseChangelogKeepsLaterAndEditedNotes(t *testing.T) {
	source := "# Changelog\n\n## [Unreleased]\n\n### Changed\n\n- **one:** First change.\n\n- **two:** Second change.\n\n## [v0.1.0]\n\n- Older.\n"
	released, err := rewriteUnreleasedToVersion(source, "v0.2.0", "2026-01-01")
	if err != nil {
		t.Fatal(err)
	}
	main := strings.Replace(source, "First change.", "First change. Further explanation.", 1)
	main = strings.Replace(main, "### Changed", "### Added\n\n- **three:** Later change.\n\n### Changed", 1)
	main = strings.Replace(main, "- Older.", "- Older with a corrected description.", 1)
	body, err := releaseChangelogOnMain(source, released, main, "v0.2.0")
	if err != nil {
		t.Fatal(err)
	}
	_, _, pending, err := changelogSectionBounds(body, "Unreleased")
	if err != nil || len(pending.Entries) != 2 {
		t.Fatalf("pending notes = %+v, %v", pending, err)
	}
	for _, want := range []string{"Further explanation.", "Later change.", "Older with a corrected description."} {
		if !strings.Contains(body, want) {
			t.Fatalf("lost %q: %s", want, body)
		}
	}
	start, end, _, err := changelogSectionBounds(body, "v0.2.0")
	cutStart, cutEnd, _, cutErr := changelogSectionBounds(released, "v0.2.0")
	if err != nil || cutErr != nil || body[start:end] != released[cutStart:cutEnd] {
		t.Fatalf("release section changed: %s (%v)", body, err)
	}
}
