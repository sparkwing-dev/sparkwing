package main

import (
	"embed"
	"fmt"
	"strings"
)

//go:embed config/agents/*.md config/rules/*.md
var configFS embed.FS

// agentDef wires a reviewer to its persona and, for the rule-enforcer
// reviewers, the markdown rules file it polices. Model is the claude
// alias the reviewer runs under.
type agentDef struct {
	Name    string
	Persona string // path within configFS
	Rules   string // optional path within configFS
	Model   string
}

// agents is the regulation roster. Rule-enforcers read a rules file from
// the registry (edit the markdown to change what they enforce, no code
// change); the judgment and finder reviewers carry their whole mandate in
// the persona.
var agents = []agentDef{
	{Name: "cli-reviewer", Persona: "config/agents/cli-reviewer.md", Rules: "config/rules/cli.md", Model: "opus"},
	{Name: "sdk-reviewer", Persona: "config/agents/sdk-reviewer.md", Rules: "config/rules/sdk.md", Model: "opus"},
	{Name: "architecture-reviewer", Persona: "config/agents/architecture-reviewer.md", Rules: "config/rules/architecture.md", Model: "opus"},
	{Name: "tests-reviewer", Persona: "config/agents/tests-reviewer.md", Rules: "config/rules/tests.md", Model: "opus"},
	{Name: "api-surface-reviewer", Persona: "config/agents/api-surface-reviewer.md", Model: "opus"},
	{Name: "docs-reviewer", Persona: "config/agents/docs-reviewer.md", Model: "opus"},
	{Name: "release-notes-reviewer", Persona: "config/agents/release-notes-reviewer.md", Model: "opus"},
	{Name: "comment-reviewer", Persona: "config/agents/comment-reviewer.md", Model: "opus"},
	{Name: "correctness-reviewer", Persona: "config/agents/correctness-reviewer.md", Model: "opus"},
	{Name: "security-reviewer", Persona: "config/agents/security-reviewer.md", Model: "opus"},
}

// filterAgents returns the reviewers named in a comma-separated list,
// preserving roster order. Unknown names are ignored; the caller reports
// an empty result.
func filterAgents(csv string) []agentDef {
	want := map[string]bool{}
	for n := range strings.SplitSeq(csv, ",") {
		if n = strings.TrimSpace(n); n != "" {
			want[n] = true
		}
	}
	var out []agentDef
	for _, a := range agents {
		if want[a.Name] {
			out = append(out, a)
		}
	}
	return out
}

func agentNames() []string {
	names := make([]string, len(agents))
	for i, a := range agents {
		names[i] = a.Name
	}
	return names
}

// systemPrompt assembles the reviewer's appended system prompt: its
// persona, plus the rules file inlined for rule-enforcers.
func (a agentDef) systemPrompt() (string, error) {
	persona, err := configFS.ReadFile(a.Persona)
	if err != nil {
		return "", fmt.Errorf("read persona %s: %w", a.Persona, err)
	}
	var b strings.Builder
	b.Write(persona)
	if a.Rules != "" {
		rules, err := configFS.ReadFile(a.Rules)
		if err != nil {
			return "", fmt.Errorf("read rules %s: %w", a.Rules, err)
		}
		b.WriteString("\n\n# The rules you enforce\n\nReview the diff strictly against the rules below. Each rule violation is a finding; if a rule is not implicated by the diff, ignore it.\n\n")
		b.Write(rules)
	}
	return b.String(), nil
}
