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
}

// Sections splits a doc into its headed parts. A doc with no headings
// yields one preamble section covering the whole body, so every doc is
// addressable the same way.
//
// A `#` opening a line inside a fenced code block would be read as a
// heading. Tracking fences costs more than it saves on this corpus:
// the generated references put fenced blocks under headings, never the
// reverse, so the failure mode is a spurious split rather than a lost
// section.
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

	for i, line := range lines {
		level, text, isHeading := parseHeading(line)
		if isHeading {
			flush(i)
			cur = Section{Slug: slug, Heading: text, Level: level, StartLine: i + 1}
		}
		buf = append(buf, line)
	}
	flush(len(lines))
	return out
}

// parseHeading recognizes an ATX heading (#, ##, ...).
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

// SearchSections returns the sections matching every token in query,
// best first.
//
// A heading hit outranks a body hit: someone searching "ApprovalConfig"
// wants the section that defines it, not the six that mention it in
// passing. Within a rank, shorter sections win, because a match in a
// tight section is a more precise answer than the same match inside a
// long one.
func SearchSections(query string) []Section {
	tokens := strings.Fields(strings.ToLower(strings.TrimSpace(query)))
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
			score, all := 0, true
			for _, tok := range tokens {
				switch {
				case strings.Contains(head, tok):
					score += 10
				case strings.Contains(hay, tok):
					score++
				default:
					all = false
				}
				if !all {
					break
				}
			}
			if all {
				hits = append(hits, scored{Section: s, score: score})
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
