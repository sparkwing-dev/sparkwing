package docsweb

import (
	"regexp"
	"strings"
	"testing"

	"github.com/sparkwing-dev/sparkwing/pkg/docs"
)

func noLinks(string) (string, bool) { return "", false }

func render(t *testing.T, md string) string {
	t.Helper()
	return string(renderMarkdown(md, noLinks))
}

func TestHeadingsParagraphsAndListsRender(t *testing.T) {
	cases := []struct {
		name string
		md   string
		want string
	}{
		{"h1", "# Title", "<h1>Title</h1>"},
		{"h3", "### Deep", "<h3>Deep</h3>"},
		{"heading deeper than h4 is capped", "##### Deeper", "<h4>Deeper</h4>"},
		{"a hash without a space is not a heading", "#nope", "<p>#nope</p>"},
		{"wrapped lines are one paragraph", "one\ntwo", "<p>one two</p>"},
		{"bullets", "- a\n- b", "<ul>\n<li>a</li>\n<li>b</li>\n</ul>"},
		{"numbered list", "1. a\n2. b", "<ol>\n<li>a</li>\n<li>b</li>\n</ol>"},
		{"a wrapped bullet stays one item", "- a\n  continued", "<li>a continued</li>"},
		{"fenced code is escaped, not rendered", "```\n<b>x</b>\n```", "<pre><code>&lt;b&gt;x&lt;/b&gt;\n</code></pre>"},
		{"inline code and bold", "run `x` in **bold**", "<p>run <code>x</code> in <strong>bold</strong></p>"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := render(t, tc.md); !strings.Contains(got, tc.want) {
				t.Errorf("rendered %q, want it to contain %q", got, tc.want)
			}
		})
	}
}

func TestPipeTablesRender(t *testing.T) {
	md := "| Field | Type |\n|---|---|\n| `name` | string |\n"
	got := render(t, md)
	for _, want := range []string{
		"<table>", "<th>Field</th>", "<th>Type</th>",
		"<td><code>name</code></td>", "<td>string</td>", "</table>",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("rendered %q, want it to contain %q", got, want)
		}
	}
	if strings.Contains(got, "---") {
		t.Errorf("the separator row rendered as a cell: %q", got)
	}
}

func TestTableWithoutASeparatorRowIsAllData(t *testing.T) {
	got := render(t, "| a | b |\n| c | d |\n")
	if strings.Contains(got, "<th>") {
		t.Errorf("rendered a header row without a separator: %q", got)
	}
	if !strings.Contains(got, "<td>a</td>") || !strings.Contains(got, "<td>c</td>") {
		t.Errorf("rendered %q, want both rows as data", got)
	}
}

func TestHTMLCommentsAreDropped(t *testing.T) {
	md := "<!-- GENERATED; do not edit -->\n# Title\n"
	got := render(t, md)
	if strings.Contains(got, "GENERATED") {
		t.Errorf("rendered the banner comment: %q", got)
	}
	if !strings.Contains(got, "<h1>Title</h1>") {
		t.Errorf("dropped the heading with the comment: %q", got)
	}
}

func TestMultiLineHTMLCommentIsDropped(t *testing.T) {
	got := render(t, "<!-- a\nb -->\n# Title\n")
	if strings.Contains(got, "b") && !strings.Contains(got, "<h1>Title</h1>") {
		t.Errorf("comment body leaked or heading lost: %q", got)
	}
	if strings.Contains(got, "<p>b") {
		t.Errorf("the comment's second line rendered as prose: %q", got)
	}
}

func TestCrossPageLinksResolveThroughTheHandler(t *testing.T) {
	known := func(slug string) (string, bool) {
		if slug == "pipelines" {
			return "?p=pipelines", true
		}
		return "", false
	}
	got := string(renderMarkdown("See [the tour](pipelines.md) and [gone](missing.md).", known))
	if !strings.Contains(got, `<a href="?p=pipelines">the tour</a>`) {
		t.Errorf("known cross-page link did not resolve: %q", got)
	}
	if strings.Contains(got, "missing.md") || strings.Contains(got, `href="?p=missing"`) {
		t.Errorf("unknown cross-page link was published anyway: %q", got)
	}
	if !strings.Contains(got, "gone") {
		t.Errorf("unknown cross-page link dropped its text: %q", got)
	}
}

func TestAnchorOnACrossPageLinkStillResolves(t *testing.T) {
	known := func(slug string) (string, bool) { return "?p=" + slug, slug == "auth" }
	got := string(renderMarkdown("[tokens](auth.md#tokens)", known))
	if !strings.Contains(got, `<a href="?p=auth">tokens</a>`) {
		t.Errorf("rendered %q, want the anchor stripped and the page linked", got)
	}
}

func TestMarkupInAPageCannotForgeTags(t *testing.T) {
	got := render(t, "<script>alert(1)</script>")
	if strings.Contains(got, "<script>") {
		t.Errorf("a raw tag survived: %q", got)
	}
	if !strings.Contains(got, "&lt;script&gt;") {
		t.Errorf("rendered %q, want the tag escaped", got)
	}
}

func TestOnlyLocalAndHTTPLinksBecomeAnchors(t *testing.T) {
	cases := []struct {
		target string
		anchor bool
	}{
		{"/runs", true},
		{"https://sparkwing.dev", true},
		{"http://example.com", true},
		{"javascript:alert(1)", false},
		{"data:text/html,x", false},
		{"//evil.example", false},
		{`/\evil.example`, false},
		{" //evil.example", false},
	}
	for _, tc := range cases {
		t.Run(tc.target, func(t *testing.T) {
			got := render(t, "[text]("+tc.target+")")
			if hasAnchor := strings.Contains(got, "<a href="); hasAnchor != tc.anchor {
				t.Errorf("rendered %q; anchor=%v, want %v", got, hasAnchor, tc.anchor)
			}
			if !strings.Contains(got, "text") {
				t.Errorf("rendered %q, want the link text kept either way", got)
			}
		})
	}
}

func TestHashLeadingListContinuationStaysInItsItem(t *testing.T) {
	for _, marker := range []string{"- ", "1. "} {
		for _, continuation := range []string{"#1305", "#fff", "#!/bin/sh"} {
			got := render(t, marker+"before\n  "+continuation+"\n  after")
			want := "<li>before " + continuation + " after</li>"
			if !strings.Contains(got, want) || strings.Contains(got, "<p>") {
				t.Errorf("%q with %q: got %q, want item %q and no paragraph", marker, continuation, got, want)
			}
		}
	}
}

func TestShippedPagesPreserveSourceHeadings(t *testing.T) {
	closedComment := regexp.MustCompile(`(?s)<!--.*?-->`)
	heading := regexp.MustCompile(`^#{1,} `)
	pages := docs.List()
	if len(pages) == 0 {
		t.Fatal("the embedded doc set is empty, so this check compares nothing and cannot fail")
	}
	compared := 0
	for _, page := range pages {
		t.Run(page.Slug, func(t *testing.T) {
			source, err := docs.ReadRaw(page.Slug)
			if err != nil {
				t.Fatal(err)
			}
			got := render(t, source)
			if strings.TrimSpace(source) != "" && strings.TrimSpace(got) == "" {
				t.Fatal("non-empty page rendered empty")
			}
			counts := make(map[string]int)
			inCode := false
			for _, line := range strings.Split(closedComment.ReplaceAllString(source, ""), "\n") {
				line = strings.TrimSpace(line)
				if strings.HasPrefix(line, "```") {
					inCode = !inCode
					continue
				}
				if !inCode && heading.MatchString(line) {
					counts[strings.TrimSpace(render(t, line))]++
				}
			}
			compared += len(counts)
			for want, count := range counts {
				if found := strings.Count(got, want); found < count {
					t.Errorf("source heading %q appears %d times, rendered %d times", want, count, found)
				}
			}
		})
	}
	if compared == 0 {
		t.Fatalf("no heading was compared across %d pages, so this check cannot fail", len(pages))
	}
}

// TestShippedPagesCloseEveryBlockTheyOpen asks the renderer what state the
// shipped corpus leaves it in. An unclosed comment swallows every line after
// it and an unclosed fence turns the rest of the page into code, both of which
// publish a page with its second half missing and no error anywhere.
func TestShippedPagesCloseEveryBlockTheyOpen(t *testing.T) {
	pages := docs.List()
	if len(pages) == 0 {
		t.Fatal("the embedded doc set is empty, so this check reads nothing and cannot fail")
	}
	comments, fences := 0, 0
	for _, page := range pages {
		source, err := docs.ReadRaw(page.Slug)
		if err != nil {
			t.Fatalf("%s: %v", page.Slug, err)
		}
		for _, line := range strings.Split(source, "\n") {
			trimmed := strings.TrimSpace(line)
			if strings.HasPrefix(trimmed, "<!--") {
				comments++
			}
			if strings.HasPrefix(trimmed, "```") {
				fences++
			}
		}
		r := renderer{linkTarget: noLinks}
		r.run(source)
		if r.inComment {
			t.Errorf("%s opens an HTML comment it never closes; every line after it is dropped", page.Slug)
		}
		if r.inCode {
			t.Errorf("%s opens a fenced code block it never closes; the rest of the page renders as code", page.Slug)
		}
	}
	if comments == 0 {
		t.Fatalf("no comment marker across %d shipped pages, so the comment half of this check cannot bite", len(pages))
	}
	if fences == 0 {
		t.Fatalf("no code fence across %d shipped pages, so the fence half of this check cannot bite", len(pages))
	}
}

// TestShippedPagesKeepEveryTableRow counts the table rows the shipped sources
// declare and the rows the renderer emits for them. The defect that first put
// a guard on this corpus dropped rows silently, so equality is the assertion
// and the corpus, not a fixture, is what it reads.
func TestShippedPagesKeepEveryTableRow(t *testing.T) {
	pages := docs.List()
	if len(pages) == 0 {
		t.Fatal("the embedded doc set is empty, so this check reads nothing and cannot fail")
	}
	total := 0
	for _, page := range pages {
		source, err := docs.ReadRaw(page.Slug)
		if err != nil {
			t.Fatalf("%s: %v", page.Slug, err)
		}
		want := expectedTableRows(source)
		total += want
		got := strings.Count(render(t, source), "<tr>")
		if got != want {
			t.Errorf("%s declares %d table rows, rendered %d", page.Slug, want, got)
		}
	}
	if total == 0 {
		t.Fatalf("no table row across %d shipped pages, so this check cannot bite", len(pages))
	}
}

// safety: this mirrors flushTable's own rule -- pipe lines outside a fence group
// into blocks, and a block whose second line is a separator loses it to the header.
func expectedTableRows(source string) int {
	rows, block, inCode := 0, []string{}, false
	flush := func() {
		if len(block) == 0 {
			return
		}
		rows += len(block)
		if len(block) > 1 && isTableSeparator(block[1]) {
			rows--
		}
		block = nil
	}
	for _, line := range strings.Split(source, "\n") {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "```") {
			flush()
			inCode = !inCode
			continue
		}
		if inCode {
			continue
		}
		if strings.HasPrefix(trimmed, "|") {
			block = append(block, trimmed)
			continue
		}
		flush()
	}
	flush()
	return rows
}
