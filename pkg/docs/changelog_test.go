package docs

import (
	"strings"
	"testing"
)

func TestReadChangelogTopicReturnsBody(t *testing.T) {
	body, err := Read(ChangelogSlug)
	if err != nil {
		t.Fatalf("Read(%q): %v", ChangelogSlug, err)
	}
	if !strings.Contains(body, "# Changelog") {
		t.Fatalf("changelog body missing its H1; got first line %q", changelogFirstLine(body))
	}
}

func TestListIncludesChangelogTopic(t *testing.T) {
	for _, e := range List() {
		if e.Slug == ChangelogSlug {
			if e.Bytes == 0 {
				t.Fatalf("changelog entry has zero bytes")
			}
			return
		}
	}
	t.Fatalf("List() does not include the %q topic", ChangelogSlug)
}

func TestSearchFindsChangelog(t *testing.T) {
	hits := Search("changelog")
	found := false
	for _, e := range hits {
		if e.Slug == ChangelogSlug {
			found = true
		}
	}
	if !found {
		t.Fatalf("Search(\"changelog\") did not return the changelog topic")
	}
}

func TestChangelogSection_UnreleasedPresent(t *testing.T) {
	if _, ok := ChangelogSection("Unreleased"); !ok {
		t.Fatalf("expected an [Unreleased] section in the embedded changelog")
	}
}

func TestChangelogSection_UnknownVersionAbsent(t *testing.T) {
	if body, ok := ChangelogSection("v99.99.99"); ok {
		t.Fatalf("expected no section for v99.99.99, got %q", body)
	}
}

func TestChangelogSection_VPrefixEquivalence(t *testing.T) {
	version := firstReleasedVersion(t)
	withV, ok1 := ChangelogSection(version)
	withoutV, ok2 := ChangelogSection(strings.TrimPrefix(version, "v"))
	if !ok1 || !ok2 {
		t.Fatalf("section lookup failed for %q: with-v ok=%v, without-v ok=%v", version, ok1, ok2)
	}
	if withV != withoutV {
		t.Fatalf("v-prefixed and bare lookups disagree for %q", version)
	}
}

func TestChangelogHeadingMatches(t *testing.T) {
	cases := []struct {
		heading string
		want    string
		match   bool
	}{
		{"## [v0.16.3] - 2026-07-12", "0.16.3", true},
		{"## [Unreleased]", "unreleased", true},
		{"## [v0.16.3] - 2026-07-12", "0.16.2", false},
		{"## How to read this", "0.16.3", false},
	}
	for _, c := range cases {
		if got := changelogHeadingMatches(c.heading, c.want); got != c.match {
			t.Errorf("changelogHeadingMatches(%q, %q) = %v, want %v", c.heading, c.want, got, c.match)
		}
	}
}

// firstReleasedVersion returns the newest `## [vX.Y.Z]` label in the
// embedded changelog so version-specific assertions don't hard-code a
// release that later rolls off.
func firstReleasedVersion(t *testing.T) string {
	t.Helper()
	for _, line := range strings.Split(Changelog(), "\n") {
		if !strings.HasPrefix(line, "## [v") {
			continue
		}
		rest := strings.TrimPrefix(line, "## [")
		if i := strings.IndexByte(rest, ']'); i >= 0 {
			return rest[:i]
		}
	}
	t.Fatal("no released version heading found in changelog")
	return ""
}

func changelogFirstLine(s string) string {
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		return s[:i]
	}
	return s
}
