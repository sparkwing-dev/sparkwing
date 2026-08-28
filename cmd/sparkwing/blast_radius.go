package main

import (
	"sort"

	"github.com/sparkwing-dev/sparkwing/sparkwing"
)

type stepRiskFinding struct {
	NodeID string
	StepID string
	Labels []string
}

func lookupCachedRisks(sparkwingDir, pipelineName string) []stepRiskFinding {
	schemas, err := readDescribeCache(sparkwingDir)
	if err != nil || schemas == nil {
		return nil
	}
	for _, s := range schemas {
		if s.Name != pipelineName {
			continue
		}
		var out []stepRiskFinding
		for _, row := range s.RisksBySteps {
			if len(row.Labels) == 0 {
				continue
			}
			out = append(out, stepRiskFinding{
				NodeID: row.NodeID,
				StepID: row.StepID,
				Labels: row.Labels,
			})
		}
		if len(out) == 0 && len(s.Risks) > 0 {
			out = append(out, stepRiskFinding{Labels: s.Risks})
		}
		return out
	}
	return nil
}

func enforceRiskGate(
	pipelineName string,
	findings []stepRiskFinding,
	wf runFlags,
) error {
	if wf.dryRun {
		return nil
	}

	allowed := map[string]bool{}
	for _, l := range wf.allow {
		allowed[l] = true
	}

	missingByStep := map[string][]string{}
	stepOrder := []string{}
	stepNodes := map[string]string{}
	missingUnion := map[string]bool{}
	for _, f := range findings {
		for _, label := range f.Labels {
			if allowed[label] {
				continue
			}
			if _, seen := missingByStep[f.StepID]; !seen {
				stepOrder = append(stepOrder, f.StepID)
				stepNodes[f.StepID] = f.NodeID
			}
			if !contains(missingByStep[f.StepID], label) {
				missingByStep[f.StepID] = append(missingByStep[f.StepID], label)
			}
			missingUnion[label] = true
		}
	}
	if len(missingUnion) == 0 {
		return nil
	}

	allMissing := make([]string, 0, len(missingUnion))
	for l := range missingUnion {
		allMissing = append(allMissing, l)
	}
	sort.Strings(allMissing)

	var stepID string
	if len(stepOrder) > 0 {
		stepID = stepOrder[0]
	}
	return &sparkwing.RiskBlockedError{
		Pipeline:      pipelineName,
		StepID:        stepID,
		MissingLabels: allMissing,
	}
}

func contains(xs []string, s string) bool {
	for _, x := range xs {
		if x == s {
			return true
		}
	}
	return false
}
