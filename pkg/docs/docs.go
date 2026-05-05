// Package docs ships the sparkwing user-facing documentation as
// embedded markdown so the CLI is offline and version-locked to the
// running binary. Source of truth is repo-root /docs/; this package
// embeds a mirror under content/.
package docs

import (
	"bufio"
	"embed"
	"fmt"
	"io/fs"
	"path"
	"regexp"
	"sort"
	"strings"
)

//go:embed all:content
var allDocs embed.FS

// Entry describes one doc topic. Slug is what the CLI takes via
// --topic. Title and Summary are extracted from the markdown's first
// H1 / first paragraph.
type Entry struct {
	Slug    string `json:"slug"`
	Path    string `json:"path"`
	Title   string `json:"title"`
	Summary string `json:"summary"`
	Bytes   int    `json:"bytes"`
}

// List returns every embedded doc in alphabetical slug order.
func List() []Entry {
	var entries []Entry
	_ = fs.WalkDir(allDocs, "content", func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		if !strings.HasSuffix(p, ".md") {
			return nil
		}
		body, rerr := fs.ReadFile(allDocs, p)
		if rerr != nil {
			return nil
		}
		rel := strings.TrimPrefix(p, "content/")
		slug := strings.TrimSuffix(rel, ".md")
		title, summary := extractTitleSummary(body)
		entries = append(entries, Entry{
			Slug:    slug,
			Path:    rel,
			Title:   title,
			Summary: summary,
			Bytes:   len(body),
		})
		return nil
	})
	sort.Slice(entries, func(i, j int) bool { return entries[i].Slug < entries[j].Slug })
	return entries
}

// Read returns the markdown body for the given slug, with cross-doc
// markdown links rewritten into actionable CLI commands (see
// rewriteCLILinks). Returns ErrNotFound when the slug is unknown.
func Read(slug string) (string, error) {
	slug = strings.TrimSuffix(slug, ".md")
	p := path.Join("content", slug+".md")
	body, err := fs.ReadFile(allDocs, p)
	if err != nil {
		return "", fmt.Errorf("docs: %q: %w", slug, ErrNotFound)
	}
	return rewriteCLILinks(string(body)), nil
}

// crossDocLinkPattern matches markdown links to a *.md file with an
// optional fragment.
var crossDocLinkPattern = regexp.MustCompile(`\[([^\]]+)\]\(([^)#]+)\.md(?:#[^)]*)?\)`)

// rewriteCLILinks transforms `[text](slug.md)` markdown links into
// `sparkwing docs read --topic <slug>` when slug is a known topic.
// Unknown slugs are left unchanged. Anchors are dropped (the CLI
// verb has no fragment support).
//
// Output shapes:
//   - Link text equals the filename -> bare command.
//   - Link text differs -> original text + command in parens.
func rewriteCLILinks(body string) string {
	knownSlugs := make(map[string]struct{})
	for _, e := range List() {
		knownSlugs[e.Slug] = struct{}{}
	}
	return crossDocLinkPattern.ReplaceAllStringFunc(body, func(match string) string {
		m := crossDocLinkPattern.FindStringSubmatch(match)
		if len(m) != 3 {
			return match
		}
		text, slug := m[1], m[2]
		if _, ok := knownSlugs[slug]; !ok {
			return match
		}
		cmd := "`sparkwing docs read --topic " + slug + "`"
		if text == slug || text == slug+".md" {
			return cmd
		}
		return text + " (" + cmd + ")"
	})
}

// All returns every doc concatenated with ASCII separators in
// List() order.
func All() string {
	var b strings.Builder
	for _, e := range List() {
		body, err := Read(e.Slug)
		if err != nil {
			continue
		}
		b.WriteString("\n========================================\n")
		b.WriteString("# DOC: ")
		b.WriteString(e.Slug)
		b.WriteByte('\n')
		b.WriteString("========================================\n\n")
		b.WriteString(strings.TrimSpace(body))
		b.WriteString("\n")
	}
	return b.String()
}

// Search returns entries whose title, slug, or body contain every
// space-separated token in query (case-insensitive). Title/slug hits
// rank above body-only matches. Empty query returns List().
func Search(query string) []Entry {
	query = strings.TrimSpace(query)
	if query == "" {
		return List()
	}
	tokens := strings.Fields(strings.ToLower(query))
	type scored struct {
		Entry
		score int
	}
	var hits []scored
	for _, e := range List() {
		body, err := Read(e.Slug)
		if err != nil {
			continue
		}
		hay := strings.ToLower(e.Title + " " + e.Slug + " " + body)
		titleHay := strings.ToLower(e.Title + " " + e.Slug)

		var score int
		all := true
		for _, tok := range tokens {
			if !strings.Contains(hay, tok) {
				all = false
				break
			}
			if strings.Contains(titleHay, tok) {
				score += 10
			} else {
				score += 1
			}
		}
		if !all {
			continue
		}
		hits = append(hits, scored{Entry: e, score: score})
	}
	sort.Slice(hits, func(i, j int) bool {
		if hits[i].score != hits[j].score {
			return hits[i].score > hits[j].score
		}
		return hits[i].Slug < hits[j].Slug
	})
	out := make([]Entry, len(hits))
	for i, h := range hits {
		out[i] = h.Entry
	}
	return out
}

// ErrNotFound signals an unknown slug.
type docsError string

func (e docsError) Error() string { return string(e) }

const ErrNotFound = docsError("doc not found")

// extractTitleSummary pulls the first H1 as Title and the first
// non-empty paragraph after it as Summary. Skips blockquote status
// banners. Falls back to the first non-empty line when there's no H1.
func extractTitleSummary(body []byte) (title, summary string) {
	scanner := bufio.NewScanner(strings.NewReader(string(body)))
	scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	state := 0 // 0: looking for title, 1: looking for summary, 2: collecting summary
	var summaryLines []string
	for scanner.Scan() {
		line := scanner.Text()
		trim := strings.TrimSpace(line)
		switch state {
		case 0:
			if strings.HasPrefix(trim, "# ") {
				title = strings.TrimSpace(strings.TrimPrefix(trim, "#"))
				state = 1
			} else if title == "" && trim != "" && !strings.HasPrefix(trim, "<!--") {
				title = trim
			}
		case 1:
			if trim == "" || strings.HasPrefix(trim, "<!--") || strings.HasPrefix(trim, ">") {
				continue
			}
			summaryLines = append(summaryLines, trim)
			state = 2
		case 2:
			if trim == "" {
				return title, strings.Join(summaryLines, " ")
			}
			if strings.HasPrefix(trim, "#") {
				return title, strings.Join(summaryLines, " ")
			}
			summaryLines = append(summaryLines, trim)
		}
	}
	return title, strings.Join(summaryLines, " ")
}
