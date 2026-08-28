package docs

import (
	"fmt"
	"sort"
	"strings"
)

// A Guide is a named set of topics that answer one task together.
//
// It exists because the corpus is addressed one topic at a time, and
// several tasks need three or four topics before anything can be
// written. Recorded agent trials authoring a pipeline read
// authoring-pipelines, pipelines, hooks, and config-reference in
// separate calls on nearly every run, each round trip costing seconds
// and none of them optional.
//
// A guide is deliberately not a document. It owns no prose, only an
// ordered list of topics, so it cannot drift from what it points at:
// edit a topic and the guide reflects it.
type Guide struct {
	// Name is the guide slug, passed as `docs read --guide <name>`.
	Name string
	// Summary is one line: which task this guide is for.
	Summary string
	// Topics are the slugs, in reading order.
	Topics []string
}

var guides = []Guide{
	{
		Name:    "authoring",
		Summary: "Write a pipeline: the DAG model, the idioms the linter enforces, how it fires, and the config it lands in",
		Topics: []string{
			"authoring-pipelines",
			"pipelines",
			"hooks",
			"config-reference",
		},
	},
}

// Guides returns every guide, sorted by name.
func Guides() []Guide {
	out := make([]Guide, len(guides))
	copy(out, guides)
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

// GuideByName returns the named guide.
func GuideByName(name string) (Guide, bool) {
	for _, g := range guides {
		if strings.EqualFold(g.Name, name) {
			return g, true
		}
	}
	return Guide{}, false
}

// GuideNames returns every guide name, sorted -- for error messages
// that would otherwise leave the reader guessing.
func GuideNames() []string {
	out := make([]string, 0, len(guides))
	for _, g := range guides {
		out = append(out, g.Name)
	}
	sort.Strings(out)
	return out
}

// ReadGuide concatenates a guide's topics in reading order, each under
// the same separator `docs all` uses, so a caller that already parses
// one parses the other.
func ReadGuide(name string) (string, error) {
	g, ok := GuideByName(name)
	if !ok {
		return "", docsError(fmt.Sprintf("unknown guide %q (have: %s)", name, strings.Join(GuideNames(), ", ")))
	}
	var b strings.Builder
	fmt.Fprintf(&b, "<!-- guide: %s -- %s -->\n", g.Name, g.Summary)
	fmt.Fprintf(&b, "<!-- topics: %s -->\n", strings.Join(g.Topics, ", "))
	for _, slug := range g.Topics {
		body, err := Read(slug)
		if err != nil {
			return "", docsError(fmt.Sprintf("guide %q names missing topic %q", g.Name, slug))
		}
		b.WriteString("\n========================================\n")
		b.WriteString("# DOC: ")
		b.WriteString(slug)
		b.WriteByte('\n')
		b.WriteString("========================================\n\n")
		b.WriteString(strings.TrimSpace(body))
		b.WriteString("\n")
	}
	return b.String(), nil
}
