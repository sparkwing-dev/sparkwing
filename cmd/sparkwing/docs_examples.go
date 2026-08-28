package main

import (
	"fmt"
	"sort"
	"strings"

	templates "github.com/sparkwing-dev/sparks-core/templates"

	"github.com/sparkwing-dev/sparkwing/pkg/color"
)

type exampleHit struct {
	Name    string `json:"name"`
	Summary string `json:"summary"`
	score   int
}

func searchExamples(query string) []exampleHit {
	tokens := strings.Fields(strings.ToLower(strings.TrimSpace(query)))
	if len(tokens) == 0 {
		return nil
	}
	list, err := templates.List()
	if err != nil {
		return nil
	}
	var hits []exampleHit
	for _, t := range list {
		m := t.Manifest
		summary := strings.Join(strings.Fields(firstSentence(m.WhenToUse, m.Description)), " ")
		name := strings.ToLower(m.Name)
		hay := strings.ToLower(name + " " + m.WhenToUse + " " + m.Description + " " +
			m.Applicability.Category + " " + strings.Join(m.Applicability.Cloud, " "))

		score, all := 0, true
		for _, tok := range tokens {
			switch {
			case strings.Contains(name, tok):
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
			hits = append(hits, exampleHit{Name: m.Name, Summary: summary, score: score})
		}
	}
	sort.SliceStable(hits, func(i, j int) bool {
		if hits[i].score != hits[j].score {
			return hits[i].score > hits[j].score
		}
		return hits[i].Name < hits[j].Name
	})
	return hits
}

func firstSentence(candidates ...string) string {
	for _, c := range candidates {
		c = strings.TrimSpace(c)
		if c == "" {
			continue
		}
		c = strings.Join(strings.Fields(c), " ")
		if i := strings.Index(c, ". "); i > 0 {
			return c[:i]
		}
		return c
	}
	return ""
}

func printExampleHits(hits []exampleHit, limit int) {
	if len(hits) == 0 {
		return
	}
	if limit > 0 && len(hits) > limit {
		hits = hits[:limit]
	}
	fmt.Println(color.Bold("EXAMPLES") + color.Dim("  (working pipelines -- read one, do not scaffold from it)"))
	for _, h := range hits {
		fmt.Printf("  %-32s %s\n", color.Bold(h.Name), color.Dim(truncateLine(h.Summary)))
	}
	fmt.Printf("  %s\n\n", color.Cyan("sparkwing examples --name <name> --body"))
}
