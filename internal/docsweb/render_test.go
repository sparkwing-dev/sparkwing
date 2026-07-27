package docsweb

import (
	"strings"
	"testing"
)

// noLinks resolves nothing, so a case that does not care about cross-page
// links gets the unlinked rendering.
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

// Reference pages are mostly tables -- config-reference.md is nothing else --
// so a renderer that dropped them would publish those pages unreadable.
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

// Every generated page opens with a "do not edit by hand" banner comment, so a
// renderer that escaped comments would put that banner in front of the reader.
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

// The set cross-references itself by filename. In a browser those have to
// become links into this handler, or every one of them 404s.
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

// The pages are third-party-authored markdown as far as this renderer is
// concerned, and it emits HTML, so the escaping is a boundary rather than a
// nicety.
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
