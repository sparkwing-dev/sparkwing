package jobs

import (
	"bytes"
	"errors"
	"fmt"
	"path"
	"slices"
	"strings"

	"go.yaml.in/yaml/v3"
)

type reviewPolicy struct {
	Version   int `yaml:"version"`
	Objective struct {
		Sources []string `yaml:"sources"`
	} `yaml:"objective"`
	Rules      []reviewRule               `yaml:"rules"`
	Strict     []strictPaths              `yaml:"strict"`
	DocsOnly   docsOnlyRoute              `yaml:"docs_only"`
	NeverBlock []string                   `yaml:"never_block"`
	Reviewers  map[string]reviewerProfile `yaml:"reviewers"`
	Rereview   struct {
		SameReviewer    bool   `yaml:"same_reviewer"`
		SkipAllowedFrom string `yaml:"skip_allowed_from"`
	} `yaml:"rereview"`
}

type reviewRule struct {
	ID   string `yaml:"id"`
	From string `yaml:"from"`
	Text string `yaml:"text"`
}

type strictPaths struct {
	Name     string   `yaml:"name"`
	Paths    []string `yaml:"paths"`
	BlockAt  string   `yaml:"block_at"`
	Reviewer string   `yaml:"reviewer"`
}

type docsOnlyRoute struct {
	Paths    []string `yaml:"paths"`
	Reviewer string   `yaml:"reviewer"`
}

type reviewerProfile struct {
	Model string `yaml:"model"`
}

const (
	defaultReviewer   = "default"
	standardsRuleFrom = "overwing-standards"
	findingOpen       = "open"
	findingFixed      = "fixed"
	findingSkipped    = "skipped"
	resultPass        = "pass"
	resultBlock       = "block"
)

var objectiveSources = map[string]bool{"ticket": true, "commit-message": true, "brief": true}

// safety: unknown keys fail the load, because a mistyped strict entry or
// block_at would otherwise loosen review without anyone noticing.
func parseReviewPolicy(data []byte) (reviewPolicy, error) {
	var p reviewPolicy
	dec := yaml.NewDecoder(bytes.NewReader(data))
	dec.KnownFields(true)
	if err := dec.Decode(&p); err != nil {
		return reviewPolicy{}, fmt.Errorf("parse .review.yaml: %w", err)
	}
	if p.Version != 1 {
		return reviewPolicy{}, fmt.Errorf(".review.yaml: version %d is not supported; this job reads version 1", p.Version)
	}
	if len(p.Objective.Sources) == 0 {
		p.Objective.Sources = []string{"brief", "ticket", "commit-message"}
	}
	for _, s := range p.Objective.Sources {
		if !objectiveSources[s] {
			return reviewPolicy{}, fmt.Errorf(".review.yaml: objective source %q is not one of ticket, commit-message, brief", s)
		}
	}
	if p.Reviewers[defaultReviewer].Model == "" {
		return reviewPolicy{}, errors.New(".review.yaml: reviewers.default.model is required")
	}
	if p.Rereview.SkipAllowedFrom == "" {
		p.Rereview.SkipAllowedFrom = "P2"
	}
	if r := severityRank(p.Rereview.SkipAllowedFrom); r < 2 {
		return reviewPolicy{}, fmt.Errorf(".review.yaml: skip_allowed_from %q must be P2 or P3; only the reviewer clears P0 and P1", p.Rereview.SkipAllowedFrom)
	}
	for _, r := range p.Rules {
		if r.ID == "" || (r.Text == "" && r.From == "") {
			return reviewPolicy{}, fmt.Errorf(".review.yaml: rule %q needs an id and either text or from", r.ID)
		}
		if r.From != "" && r.From != standardsRuleFrom {
			return reviewPolicy{}, fmt.Errorf(".review.yaml: rule %q: from %q is not supported; use %s", r.ID, r.From, standardsRuleFrom)
		}
	}
	if len(p.DocsOnly.Paths) > 0 {
		if p.Reviewers[p.DocsOnly.Reviewer].Model == "" {
			return reviewPolicy{}, fmt.Errorf(".review.yaml: docs_only names reviewer %q, which reviewers does not define", p.DocsOnly.Reviewer)
		}
		if err := checkPatterns(p.DocsOnly.Paths); err != nil {
			return reviewPolicy{}, fmt.Errorf(".review.yaml: docs_only: %w", err)
		}
	}
	for i := range p.Strict {
		s := &p.Strict[i]
		if s.Name == "" {
			s.Name = strings.Join(s.Paths, ",")
		}
		if severityRank(s.BlockAt) < 0 {
			return reviewPolicy{}, fmt.Errorf(".review.yaml: strict %q: block_at %q is not P0 to P3", s.Name, s.BlockAt)
		}
		if s.Reviewer == "" {
			s.Reviewer = defaultReviewer
		}
		if p.Reviewers[s.Reviewer].Model == "" {
			return reviewPolicy{}, fmt.Errorf(".review.yaml: strict %q names reviewer %q, which reviewers does not define", s.Name, s.Reviewer)
		}
		if err := checkPatterns(s.Paths); err != nil {
			return reviewPolicy{}, fmt.Errorf(".review.yaml: strict %q: %w", s.Name, err)
		}
	}
	return p, nil
}

func checkPatterns(patterns []string) error {
	for _, pat := range patterns {
		for _, seg := range strings.Split(pat, "/") {
			if _, err := path.Match(seg, ""); err != nil {
				return fmt.Errorf("bad path pattern %q", pat)
			}
		}
	}
	return nil
}

// safety: docs_only swaps only the default reviewer's model, so every change is
// still reviewed; an empty diff keeps the default model.
func (p reviewPolicy) defaultModel(files []string) string {
	if len(p.DocsOnly.Paths) == 0 || len(files) == 0 {
		return p.Reviewers[defaultReviewer].Model
	}
	for _, f := range files {
		if !slices.ContainsFunc(p.DocsOnly.Paths, func(pat string) bool { return globMatch(pat, f) }) {
			return p.Reviewers[defaultReviewer].Model
		}
	}
	return p.Reviewers[p.DocsOnly.Reviewer].Model
}

func severityRank(s string) int {
	switch strings.ToUpper(strings.TrimSpace(s)) {
	case "P0":
		return 0
	case "P1":
		return 1
	case "P2":
		return 2
	case "P3":
		return 3
	}
	return -1
}

func globMatch(pattern, name string) bool {
	return globSegments(strings.Split(pattern, "/"), strings.Split(name, "/"))
}

func globSegments(pat, name []string) bool {
	for len(pat) > 0 {
		if pat[0] == "**" {
			for i := 0; i <= len(name); i++ {
				if globSegments(pat[1:], name[i:]) {
					return true
				}
			}
			return false
		}
		if len(name) == 0 {
			return false
		}
		if ok, err := path.Match(pat[0], name[0]); err != nil || !ok {
			return false
		}
		pat, name = pat[1:], name[1:]
	}
	return len(name) == 0
}

func findingPath(file string) string {
	if i := strings.Index(file, ":"); i >= 0 {
		return file[:i]
	}
	return file
}

func (p reviewPolicy) strictFor(file string) []strictPaths {
	var out []strictPaths
	name := findingPath(file)
	for _, s := range p.Strict {
		for _, pat := range s.Paths {
			if globMatch(pat, name) {
				out = append(out, s)
				break
			}
		}
	}
	return out
}

func (p reviewPolicy) blocks(f reviewFinding) bool {
	rank := severityRank(f.Severity)
	if rank <= 1 {
		return true
	}
	for _, s := range p.strictFor(f.File) {
		if rank <= severityRank(s.BlockAt) {
			return true
		}
	}
	return false
}

func (p reviewPolicy) skippable(f reviewFinding) bool {
	return severityRank(f.Severity) >= severityRank(p.Rereview.SkipAllowedFrom) && !p.blocks(f)
}

func (p reviewPolicy) strictReviewers(files []string) map[string][]string {
	out := map[string][]string{}
	for _, f := range files {
		seen := map[string]bool{}
		for _, s := range p.strictFor(f) {
			if s.Reviewer == defaultReviewer || seen[s.Reviewer] {
				continue
			}
			seen[s.Reviewer] = true
			out[s.Reviewer] = append(out[s.Reviewer], f)
		}
	}
	return out
}
