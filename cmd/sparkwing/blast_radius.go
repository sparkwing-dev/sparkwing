package main

import (
	"errors"
	"fmt"
	"sort"
	"strings"

	"github.com/sparkwing-dev/sparkwing/internal/nodeproto"
	"github.com/sparkwing-dev/sparkwing/internal/runspec"
)

type stepRiskFinding struct {
	NodeID string
	StepID string
	Labels []string
}

type riskBlockedError struct {
	Pipeline      string
	StepID        string
	MissingLabels []string
}

func (e *riskBlockedError) Error() string {
	return fmt.Sprintf("step %q in pipeline %q requires --sw-allow-risk %s to confirm (or --sw-dry-run to preview).", e.StepID, e.Pipeline, strings.Join(e.MissingLabels, ","))
}

func risksIn(schema nodeproto.PipelineDescription) []stepRiskFinding {
	var out []stepRiskFinding
	for _, row := range schema.RisksByStep {
		if len(row.Labels) == 0 {
			continue
		}
		out = append(out, stepRiskFinding{NodeID: row.JobID, StepID: row.StepID, Labels: row.Labels})
	}
	if len(out) == 0 && len(schema.Risks) > 0 {
		out = append(out, stepRiskFinding{Labels: schema.Risks})
	}
	return out
}

// safety: a narrowed run defers to plan acceptance, the first point that knows
// which jobs and steps its selection keeps.
func enforceRiskGate(
	pipelineName string,
	findings []stepRiskFinding,
	wf runFlags,
) error {
	if wf.dryRun || wf.only != "" || wf.startAt != "" || wf.stopAt != "" {
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
	return &riskBlockedError{
		Pipeline:      pipelineName,
		StepID:        stepID,
		MissingLabels: allMissing,
	}
}

func riskFlags(options runspec.Options) runFlags {
	return runFlags{
		allow: options.AllowRisk.Value, dryRun: options.DryRun.Value,
		only: options.Only.Value, startAt: options.StartAt.Value, stopAt: options.StopAt.Value,
	}
}

// hack: the gate composes the refusal so incomplete discovery names labels
// instead of withholding the whole message.
func riskRefusal(gate *runspec.DeclarationGate, err error) error {
	var blocked *riskBlockedError
	if errors.As(err, &blocked) {
		return gate.RefuseRisk(blocked.Pipeline, blocked.StepID, blocked.MissingLabels)
	}
	return gate.Error(err)
}

func contains(xs []string, s string) bool {
	for _, x := range xs {
		if x == s {
			return true
		}
	}
	return false
}
