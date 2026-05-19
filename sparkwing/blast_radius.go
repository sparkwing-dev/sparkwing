package sparkwing

import (
	"sort"
	"strings"
)

// RiskBlockedError is the typed error returned when the dispatcher
// refuses a run because one or more reachable steps declare risk
// labels the operator hasn't acknowledged via --sw-allow (or
// --sw-dry-run, which bypasses every gate). MissingLabels is the
// full set the operator must authorize, sorted, so a single retry
// with `--sw-allow a,b,c` satisfies the gate in one shot.
//
// JSON consumers (agents, dashboard) can pattern-match on the typed
// fields rather than parsing the message.
type RiskBlockedError struct {
	Pipeline      string
	StepID        string
	MissingLabels []string
}

func (e *RiskBlockedError) Error() string {
	return "step " + quote(e.StepID) + " in pipeline " + quote(e.Pipeline) +
		" requires --sw-allow " + strings.Join(e.MissingLabels, ",") +
		" to confirm (or --sw-dry-run to preview)."
}

func quote(s string) string { return `"` + s + `"` }

// Risk marks a WorkStep as gated by the given operator-acknowledged
// labels. The dispatcher refuses to run a pipeline containing such a
// step unless every label is listed in --sw-allow (or --sw-dry-run
// bypasses every gate).
//
// Labels are author-defined kebab-case strings. Conventional ones
// include "destructive", "prod", "money", but authors are free to
// invent ("rotates-key", "kicks-everyone-off-vpn").
//
//	sparkwing.Step(w, "destroy-eks", j.destroyEKS).
//	    Risk("destructive", "prod")
func (s *WorkStep) Risk(labels ...string) *WorkStep {
	for _, l := range labels {
		l = strings.TrimSpace(l)
		if l == "" {
			continue
		}
		s.addRisk(l)
	}
	return s
}

// Risks returns the labels declared on this step in declaration order
// with duplicates collapsed. Empty when no label was declared -- the
// canonical "no gate fires" state.
func (s *WorkStep) Risks() []string {
	out := make([]string, len(s.risks))
	copy(out, s.risks)
	return out
}

func (s *WorkStep) addRisk(label string) {
	for _, existing := range s.risks {
		if existing == label {
			return
		}
	}
	s.risks = append(s.risks, label)
}

// SortedUnique returns the deduplicated, lexicographically sorted
// union of the provided label slices. Used by validators to render
// stable error messages and by describe to emit a stable wire shape.
func SortedUniqueRisks(slices ...[]string) []string {
	seen := map[string]bool{}
	for _, sl := range slices {
		for _, l := range sl {
			if l == "" || seen[l] {
				continue
			}
			seen[l] = true
		}
	}
	out := make([]string, 0, len(seen))
	for l := range seen {
		out = append(out, l)
	}
	sort.Strings(out)
	return out
}
