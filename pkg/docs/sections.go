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
	// stack[d] is the innermost heading seen at depth d, so a new
	// heading's ancestors are everything shallower still standing.
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
				// Skip the H1: it is the doc title, which the slug
				// already carries, so repeating it costs width in every
				// result line and distinguishes nothing.
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

// fenceMarker returns the run of backticks or tildes opening or closing
// a fenced block, or "" for an ordinary line.
//
// The run is returned rather than a bool so a nested fence closes at
// the right depth: a ``` inside a ````-fenced block is content, and a
// closing fence must be at least as long as the one that opened it.
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

// nonCurrentPenalty sinks documents that describe something other than
// current behavior below every reference hit.
//
// A proposal records what someone wanted to build, tagged with a status
// that is frequently "draft" or "not implemented"; a migration guide
// records what changed between two versions. Ranked alongside reference
// pages they are worse than noise, because both tend to be short and
// the tie-break favors tight sections -- searching "trigger" returned a
// redesign sketch above the schema that exists. Both stay searchable,
// since "why is it like this" and "what changed in v0.16" are real
// questions; they just never answer "how does this work" ahead of the
// page that documents it.
func nonCurrentPenalty(slug string) int {
	if strings.HasPrefix(slug, "proposals/") || strings.HasPrefix(slug, "migrations/") {
		return 1000
	}
	return 0
}

// wholeWord reports whether tok appears in hay as a word rather than
// buried inside a longer one.
//
// The distinction decides the ranking. "command" is a substring of
// "Subcommands", so substring-matching a heading made the generated CLI
// reference's subcommand tables the top hit for "run shell command" --
// a list of verb names ranked above the page explaining how to run a
// shell command, because it was shorter and the heading "matched".
func wholeWord(hay, tok string) bool {
	return matchAt(hay, tok, false)
}

// wordPrefix reports whether tok starts a word in hay: "shell" matches
// "shelling", "command" does not match "subcommands".
//
// This is the cheapest stand-in for stemming, and it is what closes the
// gap between how someone asks and how a doc is titled. "How do I run a
// shell command" has to reach a section headed "Exec -- shelling out";
// requiring an exact word never gets there, and allowing any substring
// gets there via every compound noun in the corpus.
func wordPrefix(hay, tok string) bool {
	return matchAt(hay, tok, true)
}

// matchAt finds tok at a word boundary. prefixOK allows the match to end
// mid-word ("shell" in "shelling"); it must always start one.
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

// Match weights. A whole-word heading hit is the section that is *about*
// the query; a substring heading hit is usually an accident of English
// compounding, so it scores below a whole-word body hit -- the section
// that at least discusses the thing beats the one whose title merely
// contains its letters.
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

// minToken drops tokens too short to discriminate. "a" in "Exec run a
// command" matches nearly every section in the corpus, so it
// contributes only noise to the score while still narrowing nothing.
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
			// The breadcrumb scores below the heading. It says what a
			// section is *under*, not what it is about: every
			// "Subcommands" table nested beneath `sparkwing runs
			// triggers` would otherwise answer "triggers" as strongly as
			// the section that documents them.
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
