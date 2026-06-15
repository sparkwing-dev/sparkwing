// Risk-label dispatch gate. Reads the author-declared per-step
// labels from the per-repo describe cache and refuses dispatch when
// any reachable step declares a label the operator hasn't authorized
// via --sw-allow (or --sw-dry-run). Stale or missing cache silently
// degrades to "no labels detected, no gate fires" so the gate is
// purely additive and never blocks a dispatch the cache hasn't seen.
package main

import (
	"sort"

	"github.com/sparkwing-dev/sparkwing/internal/profile"
	"github.com/sparkwing-dev/sparkwing/sparkwing"
)

// stepRiskFinding is one risk-label group the gate detected on a
// specific step. The validator unions every finding's labels and
// reports the full missing set in one error so the operator can
// authorize them all in a single retry.
type stepRiskFinding struct {
	NodeID string
	StepID string
	Labels []string
}

// lookupCachedRisks returns the per-step risk-label list for the
// named pipeline as stored in the describe cache. Returns nil when
// the pipeline isn't in the cache or when reading fails -- callers
// treat empty as "no constraint declared" and proceed without
// gating, matching the venue gate's degrade-gracefully shape.
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

// enforceRiskGate is the dispatcher's gate: refuse the run when any
// reachable step declares a risk label the operator hasn't
// authorized via --sw-allow. --sw-dry-run bypasses every gate (the
// safe-mode contract), and a profile-level auto_allow can
// pre-authorize specific labels so a known-safe environment doesn't
// pester the user.
//
// Returns nil when no findings or when every declared label is
// authorized. Returns a *sparkwing.RiskBlockedError whose
// MissingLabels names every unauthorized label in one shot.
func enforceRiskGate(
	pipelineName string,
	findings []stepRiskFinding,
	wf runFlags,
	prof *profile.Profile,
) error {
	if wf.dryRun {
		return nil
	}

	_ = prof
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
