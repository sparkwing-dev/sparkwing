package jobs

import (
	"strings"
	"testing"
)

func TestInsertUnreleasedChangeWritesUnderTheChangedHeading(t *testing.T) {
	body := "# Changelog\n\n## [Unreleased]\n\n### Changed\n\n- **cache:** an earlier entry.\n\n## [v1.2.3]\n\n### Changed\n\n- old.\n"
	entry := "- **scaffold:** pins v9.9.9.\n"
	got, err := insertUnreleasedChange(body, entry)
	if err != nil {
		t.Fatal(err)
	}
	unreleased := got[strings.Index(got, "## [Unreleased]"):strings.Index(got, "## [v1.2.3]")]
	if !strings.Contains(unreleased, entry) {
		t.Fatalf("the entry did not land in the unreleased section:\n%s", unreleased)
	}
	if strings.Count(got, entry) != 1 {
		t.Errorf("the entry appears %d times", strings.Count(got, entry))
	}
	if !strings.Contains(unreleased, "- **cache:** an earlier entry.") {
		t.Error("writing the entry dropped an entry already there")
	}
}

func TestInsertUnreleasedChangeAddsTheHeadingWhenItIsAbsent(t *testing.T) {
	body := "# Changelog\n\n## [Unreleased]\n\n### Fixed\n\n- something.\n\n## [v1.2.3]\n"
	entry := "- **scaffold:** pins v9.9.9.\n"
	got, err := insertUnreleasedChange(body, entry)
	if err != nil {
		t.Fatal(err)
	}
	unreleased := got[strings.Index(got, "## [Unreleased]"):strings.Index(got, "## [v1.2.3]")]
	if !strings.Contains(unreleased, "### Changed") {
		t.Fatalf("no Changed heading was added:\n%s", unreleased)
	}
	if !strings.Contains(unreleased, entry) {
		t.Fatalf("the entry did not land in the unreleased section:\n%s", unreleased)
	}
	if !strings.Contains(unreleased, "- something.") {
		t.Error("writing the entry dropped the Fixed section's entry")
	}
}

func TestInsertUnreleasedChangeIsIdempotent(t *testing.T) {
	body := "# Changelog\n\n## [Unreleased]\n\n### Changed\n\n- **scaffold:** pins v9.9.9.\n\n## [v1.2.3]\n"
	entry := "- **scaffold:** pins v9.9.9.\n"
	got, err := insertUnreleasedChange(body, entry)
	if err != nil {
		t.Fatal(err)
	}
	if got != body {
		t.Errorf("a second bump to the same version rewrote the file:\n%s", got)
	}
}

func TestInsertUnreleasedChangeRefusesAChangelogWithNoUnreleasedSection(t *testing.T) {
	if _, err := insertUnreleasedChange("# Changelog\n\n## [v1.2.3]\n", "- x.\n"); err == nil {
		t.Fatal("a changelog with no unreleased section was accepted, so the entry would go nowhere")
	}
}
