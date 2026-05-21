package docs

import (
	"strings"
	"testing"

	"golang.org/x/mod/semver"
)

func TestMigrationsList_IncludesEmbeddedVersionsInDescendingOrder(t *testing.T) {
	entries := MigrationsList()
	if len(entries) == 0 {
		t.Fatal("MigrationsList() returned no entries; expected at least one embedded migration guide")
	}
	for i, e := range entries {
		if !semver.IsValid(e.Version) {
			t.Fatalf("entry %d has invalid semver version %q", i, e.Version)
		}
		if e.Slug != e.Version {
			t.Fatalf("entry %d slug = %q, want %q (slug mirrors the web's /migrations/index.json where slug == version)", i, e.Slug, e.Version)
		}
		if e.Bytes == 0 {
			t.Fatalf("entry %d (%s) has zero bytes; embedded file should be non-empty", i, e.Version)
		}
		if i > 0 && semver.Compare(entries[i-1].Version, e.Version) <= 0 {
			t.Fatalf("entries not in descending order: %s should come after %s",
				entries[i-1].Version, e.Version)
		}
	}
}

func TestMigrationsList_ExcludesReadmeIndex(t *testing.T) {
	for _, e := range MigrationsList() {
		if e.Slug == "README" || e.Version == "README" {
			t.Fatalf("README.md leaked into MigrationsList(): %+v", e)
		}
	}
}

func TestMigrationsList_PopulatesTitle(t *testing.T) {
	for _, e := range MigrationsList() {
		if e.Title == "" {
			t.Errorf("entry %s has empty Title; expected H1 to be parsed", e.Version)
		}
	}
}

func TestMigrationsList_PopulatesDateAndSummaryFromIndex(t *testing.T) {
	// docs/migrations/README.md has a row for v0.4.0; verify the
	// index enrichment fired.
	var got *MigrationEntry
	for i, e := range MigrationsList() {
		if e.Version == "v0.4.0" {
			got = &MigrationsList()[i]
			break
		}
	}
	if got == nil {
		t.Fatal("MigrationsList() missing v0.4.0 (the seed guide); embed may be out of sync")
	}
	if got.Date == "" {
		t.Errorf("v0.4.0 Date is empty; README.md row should have populated it")
	}
	if got.Summary == "" {
		t.Errorf("v0.4.0 Summary is empty; README.md row should have populated it")
	}
}

func TestMigrationsRead_ReturnsBody(t *testing.T) {
	body, err := MigrationsRead("v0.4.0")
	if err != nil {
		t.Fatalf("MigrationsRead(v0.4.0): %v", err)
	}
	if !strings.Contains(body, "# Migrating to v0.4.0") {
		t.Errorf("body missing expected H1 heading")
	}
}

func TestMigrationsRead_UnknownVersionErrors(t *testing.T) {
	_, err := MigrationsRead("v99.99.99")
	if err == nil {
		t.Fatal("expected error for non-embedded version")
	}
}

func TestMigrationsRead_InvalidSemverErrors(t *testing.T) {
	_, err := MigrationsRead("not-a-version")
	if err == nil {
		t.Fatal("expected error for invalid semver")
	}
}

func TestMigrationsBetween_IncludesToButExcludesFrom(t *testing.T) {
	// (v0.3.0, v0.4.0] should include v0.4.0 (which is embedded).
	entries, err := MigrationsBetween("v0.3.0", "v0.4.0")
	if err != nil {
		t.Fatalf("MigrationsBetween: %v", err)
	}
	found := false
	for _, e := range entries {
		if e.Version == "v0.4.0" {
			found = true
		}
	}
	if !found {
		t.Errorf("expected v0.4.0 in (v0.3.0, v0.4.0]; got %+v", entries)
	}

	// (v0.4.0, v0.4.0] should be empty (from is exclusive).
	entries, err = MigrationsBetween("v0.4.0", "v0.4.0")
	if err != nil {
		t.Fatalf("MigrationsBetween: %v", err)
	}
	if len(entries) != 0 {
		t.Errorf("expected empty range for (v0.4.0, v0.4.0]; got %+v", entries)
	}
}

func TestMigrationsBetween_DefaultFromIsZero(t *testing.T) {
	entries, err := MigrationsBetween("", "v0.4.0")
	if err != nil {
		t.Fatalf("MigrationsBetween: %v", err)
	}
	if len(entries) == 0 {
		t.Error("expected at least one entry with default --from")
	}
}

func TestMigrationsBetween_DefaultToIsLatest(t *testing.T) {
	entries, err := MigrationsBetween("v0.0.0", "")
	if err != nil {
		t.Fatalf("MigrationsBetween: %v", err)
	}
	if len(entries) != len(MigrationsList()) {
		t.Errorf("default --to should include every embedded guide; got %d, want %d",
			len(entries), len(MigrationsList()))
	}
}

func TestMigrationsBetween_RejectsInvalidVersions(t *testing.T) {
	if _, err := MigrationsBetween("garbage", "v0.4.0"); err == nil {
		t.Error("expected error for invalid --from")
	}
	if _, err := MigrationsBetween("v0.0.0", "garbage"); err == nil {
		t.Error("expected error for invalid --to")
	}
}

func TestMigrationsBetween_InvertedRangeReturnsEmpty(t *testing.T) {
	entries, err := MigrationsBetween("v9.0.0", "v0.4.0")
	if err != nil {
		t.Fatalf("MigrationsBetween: %v", err)
	}
	if len(entries) != 0 {
		t.Errorf("inverted range should be empty; got %+v", entries)
	}
}

func TestMigrationsBetween_AscendingOrder(t *testing.T) {
	entries, err := MigrationsBetween("v0.0.0", "")
	if err != nil {
		t.Fatalf("MigrationsBetween: %v", err)
	}
	for i := 1; i < len(entries); i++ {
		if semver.Compare(entries[i-1].Version, entries[i].Version) >= 0 {
			t.Fatalf("between output not ascending: %s before %s",
				entries[i-1].Version, entries[i].Version)
		}
	}
}

func TestMigrationsBetweenMarkdown_HasRangeHeaderAndSeparators(t *testing.T) {
	entries, err := MigrationsBetween("v0.3.0", "v0.4.0")
	if err != nil {
		t.Fatalf("MigrationsBetween: %v", err)
	}
	out, err := MigrationsBetweenMarkdown("v0.3.0", "v0.4.0", entries)
	if err != nil {
		t.Fatalf("MigrationsBetweenMarkdown: %v", err)
	}
	if !strings.HasPrefix(out, "# Migration: v0.3.0 -> v0.4.0") {
		t.Errorf("missing range header; got prefix %q", firstLine(out))
	}
	if !strings.Contains(out, "\n---\n") {
		t.Errorf("expected at least one --- separator")
	}
	if !strings.Contains(out, "Migrating to v0.4.0") {
		t.Errorf("body of v0.4.0 missing from concatenation")
	}
}

func TestMigrationsBetweenMarkdown_EmptyRangeStillRenders(t *testing.T) {
	out, err := MigrationsBetweenMarkdown("v9.0.0", "v9.9.9", nil)
	if err != nil {
		t.Fatalf("MigrationsBetweenMarkdown: %v", err)
	}
	if !strings.Contains(out, "no migration guides apply") {
		t.Errorf("expected empty-range notice; got %q", out)
	}
}

func TestParseMigrationsIndex_ExtractsKnownRow(t *testing.T) {
	idx := parseMigrationsIndex()
	row, ok := idx["v0.4.0"]
	if !ok {
		t.Fatal("v0.4.0 row not parsed from README.md")
	}
	if row.date == "" || row.summary == "" {
		t.Errorf("expected non-empty date and summary, got %+v", row)
	}
}

func firstLine(s string) string {
	before, _, _ := strings.Cut(s, "\n")
	return before
}
