package sparkwing

import "strings"

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

func (s *WorkStep) addRisk(label string) {
	for _, existing := range s.risks {
		if existing == label {
			return
		}
	}
	s.risks = append(s.risks, label)
}
