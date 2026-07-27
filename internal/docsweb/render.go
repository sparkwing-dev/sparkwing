package docsweb

import (
	"html"
	"html/template"
	"strings"
)

// renderMarkdown renders one embedded page to HTML. All text is escaped; only
// the tags this file emits are trusted.
//
// The renderer is stdlib by construction and copied per repo rather than
// shared or pulled from a markdown library. It covers ATX headings, blank-line
// paragraphs, `-` and `*` bullets, `1.` numbered lists, fenced code, pipe
// tables, HTML comments, and the inline set (`code`, **bold**,
// [text](target)). Constructs outside that subset are not interpreted: their
// markers escape and read as text, and nesting flattens. That is a safe
// failure -- the page reads, it is just less marked up than its source asks
// for -- and it is the trade the docs-publishing card's rule 8 accepts in
// return for owning the renderer.
//
// Two behaviors here are sparkwing's own and are not in the copy this was
// taken from. Pipe tables are rendered because 29 of the embedded pages carry
// one and the reference pages are mostly table -- config-reference.md is a
// lead paragraph, nine headings, and eight tables -- so serving them as flat
// text would publish those pages while making them unreadable. HTML comment lines
// are dropped because the generated pages open with a "do not edit by hand"
// banner comment that would otherwise render as the first line a reader sees.
func renderMarkdown(md string, linkTarget func(slug string) (string, bool)) template.HTML {
	r := renderer{linkTarget: linkTarget}
	return r.run(md)
}

// renderer carries the block state one pass over a page needs. It exists so
// the flush helpers can share it rather than close over a dozen locals.
type renderer struct {
	b          strings.Builder
	linkTarget func(slug string) (string, bool)

	para  []string // consecutive non-blank lines form one paragraph
	item  []string // the open list item, plus the lines it wraps onto
	table []string // consecutive pipe-table rows

	inUL, inOL, inCode, inComment bool
}

// run walks the page once, emitting each block as it closes.
//
// A line that is neither blank nor a block opener continues whatever is open:
// inside a list it is the wrap of the item above it, which read as a paragraph
// would split that item in two and put a <p> inside the list.
func (r *renderer) run(md string) template.HTML {
	for _, line := range strings.Split(md, "\n") {
		trimmed := strings.TrimSpace(line)

		if r.inComment {
			if strings.Contains(trimmed, "-->") {
				r.inComment = false
			}
			continue
		}
		if strings.HasPrefix(trimmed, "```") {
			r.flushBlocks()
			if r.inCode {
				r.b.WriteString("</code></pre>\n")
				r.inCode = false
			} else {
				r.b.WriteString("<pre><code>")
				r.inCode = true
			}
			continue
		}
		if r.inCode {
			r.b.WriteString(html.EscapeString(line))
			r.b.WriteByte('\n')
			continue
		}
		if strings.HasPrefix(trimmed, "<!--") {
			r.flushBlocks()
			if !strings.Contains(trimmed, "-->") {
				r.inComment = true
			}
			continue
		}
		if strings.HasPrefix(trimmed, "|") {
			r.flushParagraph()
			r.closeLists()
			r.table = append(r.table, trimmed)
			continue
		}
		r.flushTable()

		switch {
		case trimmed == "":
			r.flushParagraph()
			r.closeLists()
		case strings.HasPrefix(trimmed, "#"):
			r.heading(trimmed)
		case strings.HasPrefix(trimmed, "- ") || strings.HasPrefix(trimmed, "* "):
			r.bullet(trimmed[2:], false)
		case orderedMarker(trimmed) > 0:
			r.bullet(trimmed[orderedMarker(trimmed):], true)
		default:
			if r.inUL || r.inOL {
				r.item = append(r.item, trimmed)
			} else {
				r.para = append(r.para, trimmed)
			}
		}
	}
	r.flushBlocks()
	if r.inCode {
		r.b.WriteString("</code></pre>\n")
	}
	return template.HTML(r.b.String())
}

// heading writes an ATX heading, capping at h4 so a deeply nested page cannot
// emit tags the stylesheet has no rule for.
func (r *renderer) heading(trimmed string) {
	level := 0
	for level < len(trimmed) && trimmed[level] == '#' {
		level++
	}
	if level >= len(trimmed) || trimmed[level] != ' ' {
		r.para = append(r.para, trimmed)
		return
	}
	r.flushParagraph()
	r.closeLists()
	text := strings.TrimSpace(trimmed[level:])
	if level > 4 {
		level = 4
	}
	tag := "h" + string(rune('0'+level))
	r.b.WriteString("<" + tag + ">" + r.inline(text) + "</" + tag + ">\n")
}

func (r *renderer) bullet(text string, ordered bool) {
	r.flushParagraph()
	r.flushItem()
	if ordered && !r.inOL {
		r.closeLists()
		r.b.WriteString("<ol>\n")
		r.inOL = true
	}
	if !ordered && !r.inUL {
		r.closeLists()
		r.b.WriteString("<ul>\n")
		r.inUL = true
	}
	r.item = append(r.item, strings.TrimSpace(text))
}

// orderedMarker returns the length of a leading `12. ` marker, or 0.
func orderedMarker(s string) int {
	i := 0
	for i < len(s) && s[i] >= '0' && s[i] <= '9' {
		i++
	}
	if i == 0 || i+1 >= len(s) || s[i] != '.' || s[i+1] != ' ' {
		return 0
	}
	return i + 2
}

func (r *renderer) flushBlocks() {
	r.flushParagraph()
	r.closeLists()
	r.flushTable()
}

// flushParagraph emits the open paragraph, joining its lines with spaces: a
// line break in the source is not a break in the output.
func (r *renderer) flushParagraph() {
	if len(r.para) == 0 {
		return
	}
	r.b.WriteString("<p>" + r.inline(strings.Join(r.para, " ")) + "</p>\n")
	r.para = r.para[:0]
}

func (r *renderer) flushItem() {
	if len(r.item) == 0 {
		return
	}
	r.b.WriteString("<li>" + r.inline(strings.Join(r.item, " ")) + "</li>\n")
	r.item = r.item[:0]
}

func (r *renderer) closeLists() {
	r.flushItem()
	if r.inUL {
		r.b.WriteString("</ul>\n")
		r.inUL = false
	}
	if r.inOL {
		r.b.WriteString("</ol>\n")
		r.inOL = false
	}
}

// flushTable renders the collected pipe rows. A second row made only of dashes
// and colons marks the first row as a header; without it every row is data, so
// a table missing its separator still renders as a table.
func (r *renderer) flushTable() {
	if len(r.table) == 0 {
		return
	}
	rows := r.table
	r.table = nil

	header := len(rows) > 1 && isTableSeparator(rows[1])
	r.b.WriteString("<table>\n")
	for i, row := range rows {
		if header && i == 1 {
			continue
		}
		cell := "td"
		if header && i == 0 {
			cell = "th"
		}
		r.b.WriteString("<tr>")
		for _, c := range tableCells(row) {
			r.b.WriteString("<" + cell + ">" + r.inline(c) + "</" + cell + ">")
		}
		r.b.WriteString("</tr>\n")
	}
	r.b.WriteString("</table>\n")
}

func isTableSeparator(row string) bool {
	cells := tableCells(row)
	if len(cells) == 0 {
		return false
	}
	for _, c := range cells {
		if c == "" || strings.Trim(c, "-: ") != "" {
			return false
		}
	}
	return true
}

// tableCells splits a pipe row into its cells, dropping the empty cells the
// leading and trailing pipes produce.
func tableCells(row string) []string {
	parts := strings.Split(strings.TrimSpace(row), "|")
	if len(parts) > 0 && strings.TrimSpace(parts[0]) == "" {
		parts = parts[1:]
	}
	if len(parts) > 0 && strings.TrimSpace(parts[len(parts)-1]) == "" {
		parts = parts[:len(parts)-1]
	}
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		out = append(out, strings.TrimSpace(p))
	}
	return out
}

// inline escapes text, then applies `code`, **bold**, and [text](target).
// Order matters: escape first so no source character can forge a tag, and
// handle inline code before bold so a ** inside backticks stays literal.
func (r *renderer) inline(s string) string {
	s = html.EscapeString(s)
	s = replaceDelimited(s, "`", "<code>", "</code>")
	s = replaceDelimited(s, "**", "<strong>", "</strong>")
	return r.links(s)
}

// replaceDelimited wraps each run between paired delimiters, leaving an
// unpaired trailing delimiter as literal text.
func replaceDelimited(s, delim, open, closing string) string {
	var b strings.Builder
	for {
		i := strings.Index(s, delim)
		if i < 0 {
			b.WriteString(s)
			return b.String()
		}
		j := strings.Index(s[i+len(delim):], delim)
		if j < 0 {
			b.WriteString(s)
			return b.String()
		}
		b.WriteString(s[:i])
		b.WriteString(open + s[i+len(delim):i+len(delim)+j] + closing)
		s = s[i+len(delim)+j+len(delim):]
	}
}

// links turns [text](target) into an anchor, dropping the anchor for any
// target linkHref rejects so the text still reads.
func (r *renderer) links(s string) string {
	var b strings.Builder
	for {
		open := strings.IndexByte(s, '[')
		if open < 0 {
			b.WriteString(s)
			return b.String()
		}
		closeIdx := strings.IndexByte(s[open:], ']')
		if closeIdx < 0 || open+closeIdx+1 >= len(s) || s[open+closeIdx+1] != '(' {
			b.WriteString(s[:open+1])
			s = s[open+1:]
			continue
		}
		closeIdx += open
		paren := strings.IndexByte(s[closeIdx:], ')')
		if paren < 0 {
			b.WriteString(s[:closeIdx+1])
			s = s[closeIdx+1:]
			continue
		}
		paren += closeIdx
		text, target := s[open+1:closeIdx], s[closeIdx+2:paren]
		b.WriteString(s[:open])
		if href, ok := r.href(target); ok {
			b.WriteString(`<a href="` + href + `">` + text + `</a>`)
		} else {
			b.WriteString(text)
		}
		s = s[paren+1:]
	}
}

// href resolves a link target. A link to another page of the set becomes a
// link into this handler, because the set cross-references itself by filename
// and a reader in a browser should land on the page rather than on a 404 for
// a markdown file the server does not serve.
func (r *renderer) href(target string) (string, bool) {
	if slug, ok := strings.CutSuffix(strings.SplitN(target, "#", 2)[0], ".md"); ok {
		if resolved, known := r.linkTarget(slug); known {
			return resolved, true
		}
		return "", false
	}
	if !safeHref(target) {
		return "", false
	}
	return target, true
}

// safeHref allows only site-local and http(s) links. target is already
// HTML-escaped by inline, so it cannot carry a quote to break out of the
// attribute; this bars the dangerous schemes on top of that.
//
// The judgement is made on the target the browser will resolve rather than on
// the source text, so every spelling of an authority collapses to one case.
// Two leading slashes start one, and a target the leading-slash test alone
// reads as site-local lands the reader offsite.
func safeHref(target string) bool {
	target = normalizedHref(target)
	if strings.HasPrefix(target, "//") {
		return false
	}
	return strings.HasPrefix(target, "/") ||
		strings.HasPrefix(target, "https://") ||
		strings.HasPrefix(target, "http://")
}

// normalizedHref rewrites a target into the form a WHATWG URL parser reads
// before it decides where it points: surrounding C0 controls and spaces are
// trimmed, embedded tabs and newlines are removed, and a backslash counts as a
// slash under the http(s) schemes these pages are served over. That collapses
// every spelling of an offsite authority -- //host, /\host, and either with a
// leading space or an embedded tab -- onto the one leading //.
func normalizedHref(target string) string {
	target = strings.TrimFunc(target, func(r rune) bool { return r <= ' ' })
	target = strings.NewReplacer("\t", "", "\n", "", "\r", "").Replace(target)
	return strings.ReplaceAll(target, `\`, "/")
}
