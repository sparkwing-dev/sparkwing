package docs

import (
	"sort"
	"strings"
)

// A Section is one heading's worth of a doc: the heading itself plus
// everything under it, up to the next heading at the same or shallower
// depth.
//
// Sections exist because a whole doc is the wrong unit for a lookup.
// The reference pages run to tens of thousands of tokens, and agent
// trials show what that forces: read the top, grep for line numbers,
// then sed the ranges out -- three round trips to answer one question
// like "what does a pull_request trigger look like". A section is the
// unit that question actually has an answer in.
type Section struct {
	// Slug is the doc this section came from.
	Slug string `json:"slug"`
	// Heading is the heading text, without its leading #s. Empty for
	// the preamble above a doc's first heading.
	Heading string `json:"heading"`
	// Level is the heading depth (1 for #, 2 for ##). Zero for the
	// preamble.
	Level int `json:"level"`
	// StartLine and EndLine are 1-based and inclusive, so a caller that
	// wants the raw text can still reach for it.
	StartLine int `json:"start_line"`
	EndLine   int `json:"end_line"`
	// Body is the section including its heading line.
	Body string `json:"body"`
	// Breadcrumb is the enclosing headings, outermost first, joined by
	// " > ". Empty for a top-level section.
	//
	// The generated CLI reference has one "Examples" section per verb --
	// 139 of them, identically titled -- and a heading that repeats
	// verbatim across a doc identifies nothing on its own. The
	// breadcrumb is what makes "Examples" under `sparkwing pipeline run`
	// a different thing from "Examples" under `sparkwing version`, both
	// to a ranking function and to whoever reads the hit.
	Breadcrumb string `json:"breadcrumb,omitempty"`
}

// Sections splits a doc into its headed parts. A doc with no headings
// yields one preamble section covering the whole body, so every doc is
// addressable the same way.
//
// Fenced code blocks are tracked, because a `#` inside one is a
// comment, not a heading, and the corpus is full of them: naming the
// file an example belongs in (`# .sparkwing/sparkwing.yaml`) is how
// nearly every YAML block opens. 453 such lines across 22 docs, 358 in
// the generated CLI reference alone.
//
// Treating them as headings does not merely add a spurious section --
// it destroys the real one. The heading keeps the opening fence and
// nothing else, and the content it introduced becomes an orphan
// section named after a filename. An agent trial found the top hit for
// "pull request trigger" rendering as a bare ```yaml with the answer
// stranded eight places below it.
func Sections(slug string) ([]Section, error) {
	body, err := Read(slug)
	if err != nil {
		return nil, err
	}
	return splitSections(slug, body), nil
}

func splitSections(slug, body string) []Section {
	lines := strings.Split(body, "\n")
	var out []Section
	cur := Section{Slug: slug, StartLine: 1}
	var buf []string

	flush := func(end int) {
		if len(buf) == 0 && cur.Heading == "" {
			return
		}
		cur.Body = strings.TrimRight(strings.Join(buf, "\n"), "\n")
		cur.EndLine = end
		if strings.TrimSpace(cur.Body) != "" {
			out = append(out, cur)
		}
		buf = nil
	}

	fence := ""

	var stack []string
	for i, line := range lines {
		if marker := fenceMarker(line); marker != "" {
			switch {
			case fence == "":
				fence = marker
			case strings.HasPrefix(marker, fence):
				fence = ""
			}
		}
		if fence == "" {
			if level, text, isHeading := parseHeading(line); isHeading {
				flush(i)
				for len(stack) >= level {
					stack = stack[:len(stack)-1]
				}
				for len(stack) < level-1 {
					stack = append(stack, "")
				}

				var crumbs []string
				for d, a := range stack {
					if d > 0 && a != "" {
						crumbs = append(crumbs, a)
					}
				}
				cur = Section{
					Slug: slug, Heading: text, Level: level, StartLine: i + 1,
					Breadcrumb: strings.Join(crumbs, " > "),
				}
				stack = append(stack, text)
			}
		}
		buf = append(buf, line)
	}
	flush(len(lines))
	return out
}

func fenceMarker(line string) string {
	t := strings.TrimLeft(line, " ")
	for _, c := range []byte{'`', '~'} {
		n := 0
		for n < len(t) && t[n] == c {
			n++
		}
		if n >= 3 {
			return t[:n]
		}
	}
	return ""
}

func parseHeading(line string) (level int, text string, ok bool) {
	if !strings.HasPrefix(line, "#") {
		return 0, "", false
	}
	i := 0
	for i < len(line) && line[i] == '#' {
		i++
	}
	if i > 6 || i >= len(line) || line[i] != ' ' {
		return 0, "", false
	}
	return i, strings.TrimSpace(line[i:]), true
}

func nonCurrentPenalty(slug string) int {
	if strings.HasPrefix(slug, "proposals/") || strings.HasPrefix(slug, "migrations/") {
		return 1000
	}
	return 0
}

func wholeWord(hay, tok string) bool {
	return matchAt(hay, tok, false)
}

func wordPrefix(hay, tok string) bool {
	return matchAt(hay, tok, true)
}

func matchAt(hay, tok string, prefixOK bool) bool {
	for i := 0; i <= len(hay)-len(tok); {
		j := strings.Index(hay[i:], tok)
		if j < 0 {
			return false
		}
		start := i + j
		if !isWordByte(hay, start-1) && (prefixOK || !isWordByte(hay, start+len(tok))) {
			return true
		}
		i = start + 1
	}
	return false
}

func isWordByte(s string, i int) bool {
	if i < 0 || i >= len(s) {
		return false
	}
	c := s[i]
	return c == '_' || c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9'
}

const (
	scoreHeadingWord   = 20
	scoreHeadingPrefix = 12
	scoreCrumbWord     = 8
	scoreBodyWord      = 5
	scoreCrumbPrefix   = 4
	scoreBodyPrefix    = 3
	scoreHeadingSubstr = 2
	scoreBodySubstr    = 1
)

const minToken = 2

// SearchSections returns the sections matching every token in query,
// best first.
//
// A heading hit outranks a body hit: someone searching "ApprovalConfig"
// wants the section that defines it, not the six that mention it in
// passing. Within a rank, shorter sections win, because a match in a
// tight section is a more precise answer than the same match inside a
// long one -- but only as a tie-break, since a section that explains
// something is long precisely because it explains it.
func SearchSections(query string) []Section {
	tokens := strings.Fields(strings.ToLower(strings.TrimSpace(query)))
	kept := tokens[:0]
	for _, t := range tokens {
		if len(t) >= minToken {
			kept = append(kept, t)
		}
	}
	tokens = kept
	if len(tokens) == 0 {
		return nil
	}
	type scored struct {
		Section
		score int
	}
	var hits []scored
	for _, e := range List() {
		secs, err := Sections(e.Slug)
		if err != nil {
			continue
		}
		for _, s := range secs {
			hay := strings.ToLower(s.Body)
			head := strings.ToLower(s.Heading + " " + s.Slug)

			crumb := strings.ToLower(s.Breadcrumb)
			score, all := 0, true
			for _, tok := range tokens {
				switch {
				case wholeWord(head, tok):
					score += scoreHeadingWord
				case wordPrefix(head, tok):
					score += scoreHeadingPrefix
				case wholeWord(crumb, tok):
					score += scoreCrumbWord
				case wholeWord(hay, tok):
					score += scoreBodyWord
				case wordPrefix(crumb, tok):
					score += scoreCrumbPrefix
				case wordPrefix(hay, tok):
					score += scoreBodyPrefix
				case strings.Contains(head, tok):
					score += scoreHeadingSubstr
				case strings.Contains(hay, tok):
					score += scoreBodySubstr
				default:
					all = false
				}
				if !all {
					break
				}
			}
			if all {
				hits = append(hits, scored{Section: s, score: score - nonCurrentPenalty(e.Slug)})
			}
		}
	}
	sort.SliceStable(hits, func(i, j int) bool {
		if hits[i].score != hits[j].score {
			return hits[i].score > hits[j].score
		}
		li, lj := len(hits[i].Body), len(hits[j].Body)
		if li != lj {
			return li < lj
		}
		return hits[i].Slug < hits[j].Slug
	})
	out := make([]Section, len(hits))
	for i, h := range hits {
		out[i] = h.Section
	}
	return out
}
