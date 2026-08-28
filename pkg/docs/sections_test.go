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

func TestFencedCommentsAreNotHeadings(t *testing.T) {
	for _, slug := range []string{"hooks", "pipelines", "getting-started", "cli-reference", "cli-runs"} {
		t.Run(slug, func(t *testing.T) {
			secs, err := docs.Sections(slug)
			if err != nil {
				t.Fatal(err)
			}
			for _, s := range secs {

				if !strings.Contains(s.Heading, " ") && strings.Contains(s.Heading, "/") {
					t.Errorf("section heading %q is a path; a fenced comment was read as a heading", s.Heading)
				}
				if s.Heading == "" {
					continue
				}
				body := strings.TrimSpace(s.Body)
				if _, rest, ok := strings.Cut(body, "\n"); ok {
					if strings.HasPrefix(strings.TrimSpace(rest), "```") && strings.Count(rest, "```") < 2 {
						t.Errorf("section %q is a heading plus an unclosed fence -- its content was split away: %q", s.Heading, s.Body)
					}
				}
			}
		})
	}
}

func TestSectionsDoNotSplitInsideAFence(t *testing.T) {
	for _, e := range docs.List() {
		secs, err := docs.Sections(e.Slug)
		if err != nil {
			t.Fatal(err)
		}
		for _, s := range secs {
			if n := strings.Count(s.Body, "\n```"); n%2 != 0 {
				t.Errorf("%s: section %q holds %d fence markers -- the block is cut across a section boundary",
					e.Slug, s.Heading, n)
			}
		}
	}
}

func TestNonCurrentDocsNeverOutrankReference(t *testing.T) {
	for _, q := range []string{"trigger", "admission", "cache", "pipeline"} {
		hits := docs.SearchSections(q)
		if len(hits) == 0 {
			t.Errorf("SearchSections(%q) found nothing", q)
			continue
		}
		var current bool
		for _, h := range hits {
			if !strings.HasPrefix(h.Slug, "proposals/") && !strings.HasPrefix(h.Slug, "migrations/") {
				current = true
				break
			}
		}
		if !current {
			continue
		}
		if strings.HasPrefix(hits[0].Slug, "proposals/") || strings.HasPrefix(hits[0].Slug, "migrations/") {
			t.Errorf("SearchSections(%q) ranked %q first while a reference section also matched",
				q, hits[0].Slug)
		}
	}
}

func TestScaffoldTriggerQueryFindsTheSchema(t *testing.T) {
	hits := docs.SearchSections("on: trigger")
	if len(hits) == 0 {
		t.Fatal("the query named in `pipeline new` output returns nothing")
	}
	if !strings.Contains(strings.ToLower(hits[0].Heading), "trigger") {
		t.Errorf("top hit is %q/%q; expected a section about triggers", hits[0].Slug, hits[0].Heading)
	}
	if !strings.Contains(hits[0].Body, "pull_request") {
		t.Errorf("top hit for the trigger query does not name pull_request:\n%s", hits[0].Body)
	}
}

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

func TestShellCommandQueriesReachTheRightSection(t *testing.T) {
	queries := []string{
		"run shell command",
		"run shell command bash",
		"Exec run a command",
		"Bash step",
		"go test command",
	}
	for _, q := range queries {
		hits := docs.SearchSections(q)
		if len(hits) == 0 {
			t.Errorf("SearchSections(%q) found nothing", q)
			continue
		}
		head := strings.ToLower(hits[0].Heading)
		if !strings.Contains(head, "exec") && !strings.Contains(head, "bash") {
			t.Errorf("SearchSections(%q) ranked %q/%q first; expected a section about Exec/Bash",
				q, hits[0].Slug, hits[0].Heading)
		}
	}
}

func TestSingleCharacterTokensAreIgnored(t *testing.T) {
	with := docs.SearchSections("Exec run a command")
	without := docs.SearchSections("Exec run command")
	if len(with) == 0 || len(without) == 0 {
		t.Fatal("expected hits for both forms")
	}
	if with[0].Slug != without[0].Slug || with[0].Heading != without[0].Heading {
		t.Errorf("a stray %q changed the top hit: %q/%q vs %q/%q",
			"a", with[0].Slug, with[0].Heading, without[0].Slug, without[0].Heading)
	}
	if docs.SearchSections("a") != nil {
		t.Error("a query of only noise tokens should return nothing")
	}
}

func TestRepeatedHeadingsAreDisambiguatedByBreadcrumb(t *testing.T) {
	secs, err := docs.Sections("cli-runs")
	if err != nil {
		t.Fatal(err)
	}
	var examples int
	for _, s := range secs {
		if s.Heading != "Examples" {
			continue
		}
		examples++
		if s.Breadcrumb == "" {
			t.Errorf("an %q section at line %d carries no breadcrumb", s.Heading, s.StartLine)
		}
		if strings.Contains(s.Breadcrumb, "CLI reference:") {
			t.Errorf("breadcrumb %q repeats the doc title, which the slug already carries", s.Breadcrumb)
		}
	}
	if examples < 10 {
		t.Fatalf("found only %d Examples sections; the check is not exercising the case", examples)
	}
}

func TestBreadcrumbMatchesRankBelowHeadingMatches(t *testing.T) {
	hits := docs.SearchSections("Triggers")
	if len(hits) == 0 {
		t.Fatal("no hits")
	}
	if !strings.Contains(strings.ToLower(hits[0].Heading), "trigger") {
		t.Errorf("top hit %q/%q matched on its breadcrumb, not its heading",
			hits[0].Slug, hits[0].Heading)
	}
}
