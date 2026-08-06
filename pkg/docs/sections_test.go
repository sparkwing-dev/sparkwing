package docs_test

import (
	"strings"
	"testing"

	"github.com/sparkwing-dev/sparkwing/pkg/docs"
)

func TestSectionsCoverTheWholeDoc(t *testing.T) {
	secs, err := docs.Sections("hooks")
	if err != nil {
		t.Fatal(err)
	}
	if len(secs) < 5 {
		t.Fatalf("hooks split into %d sections, expected the doc's headings", len(secs))
	}
	prev := 0
	for _, s := range secs {
		if s.StartLine <= prev {
			t.Errorf("section %q starts at %d, not after the previous section", s.Heading, s.StartLine)
		}
		if s.EndLine < s.StartLine {
			t.Errorf("section %q ends (%d) before it starts (%d)", s.Heading, s.EndLine, s.StartLine)
		}
		prev = s.StartLine
		if s.Slug != "hooks" {
			t.Errorf("section carries slug %q", s.Slug)
		}
	}
}

// A section has to include its own heading: the heading is most of what
// tells a reader whether the body under it is the answer.
func TestSectionBodyIncludesItsHeading(t *testing.T) {
	secs, err := docs.Sections("hooks")
	if err != nil {
		t.Fatal(err)
	}
	for _, s := range secs {
		if s.Heading == "" {
			continue
		}
		first := strings.SplitN(s.Body, "\n", 2)[0]
		if !strings.Contains(first, s.Heading) {
			t.Errorf("section %q does not open with its heading: %q", s.Heading, first)
		}
	}
}

// The queries agents actually run are exact identifiers -- YAML keys and
// Go symbols read out of an error or a struct -- so those are what the
// ranking has to get right.
func TestSearchSectionsFindsTheDefiningSection(t *testing.T) {
	cases := []struct {
		query     string
		wantSlug  string
		wantInTop string
	}{
		{"pull_request", "hooks", "pull"},
		{"ApprovalConfig", "sdk-reference", "Approval"},
	}
	for _, tc := range cases {
		hits := docs.SearchSections(tc.query)
		if len(hits) == 0 {
			t.Errorf("SearchSections(%q) found nothing", tc.query)
			continue
		}
		top := hits[0]
		if !strings.Contains(strings.ToLower(top.Heading+" "+top.Slug), strings.ToLower(tc.wantInTop)) {
			t.Errorf("SearchSections(%q) ranked %q/%q first; expected something naming %q",
				tc.query, top.Slug, top.Heading, tc.wantInTop)
		}
	}
}

// The point of a section hit is that it is small enough to read. If the
// top hit for a precise symbol is the whole reference page, this has
// bought nothing over `docs read`.
func TestSearchSectionsReturnsSomethingSmallEnoughToRead(t *testing.T) {
	hits := docs.SearchSections("ApprovalConfig")
	if len(hits) == 0 {
		t.Fatal("no hits")
	}
	full, err := docs.Read(hits[0].Slug)
	if err != nil {
		t.Fatal(err)
	}
	if len(hits[0].Body) > len(full)/4 {
		t.Errorf("top hit is %d bytes of a %d-byte doc; that is not a section",
			len(hits[0].Body), len(full))
	}
}

func TestSearchSectionsRequiresEveryToken(t *testing.T) {
	hits := docs.SearchSections("pull_request zzzznotpresent")
	if len(hits) != 0 {
		t.Errorf("expected no hits when one token is absent, got %d", len(hits))
	}
	if got := docs.SearchSections("   "); got != nil {
		t.Errorf("blank query should return nothing, got %d hits", len(got))
	}
}

func TestSearchSectionsPrefersHeadingMatches(t *testing.T) {
	hits := docs.SearchSections("Triggers")
	if len(hits) == 0 {
		t.Fatal("no hits for Triggers")
	}
	if !strings.Contains(strings.ToLower(hits[0].Heading), "trigger") {
		t.Errorf("top hit heading %q does not match the query; heading hits should outrank body hits", hits[0].Heading)
	}
}
