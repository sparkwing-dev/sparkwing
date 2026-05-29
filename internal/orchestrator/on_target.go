package orchestrator

import (
	"fmt"
	"sort"
	"strings"

	"github.com/sparkwing-dev/sparkwing/internal/sparkwingruntime"
	"github.com/sparkwing-dev/sparkwing/pkg/pipelines"
	"github.com/sparkwing-dev/sparkwing/sparkwing"
)

// computeOnTargetSkip resolves the plan-finalize OnTarget filter. It
// walks every Job in the plan's effective target set (the union of
// the author's explicit OnTarget plus the inferred propagation from
// consumers) and records a skip-reason for every Job that does not
// match the active target. The empty target intentionally skips
// every non-universal Job -- a run without --for executes only the
// always-runs set.
//
// The returned map is keyed by Job id; nodes absent from the map
// dispatch normally.
func computeOnTargetSkip(plan *sparkwing.Plan, target string) map[string]string {
	if plan == nil {
		return nil
	}
	eff := sparkwingruntime.EffectiveJobTargets(plan)
	if len(eff) == 0 {
		return nil
	}
	out := make(map[string]string, len(eff))
	for id, set := range eff {
		if sparkwingruntime.JobAllowsTarget(set, target) {
			continue
		}
		out[id] = formatJobOnTargetSkip(set, target)
	}
	return out
}

// formatJobOnTargetSkip mirrors the WhenRunner skip message shape so
// dashboard renderers can treat OnTarget and WhenRunner skips
// uniformly.
func formatJobOnTargetSkip(effective []string, target string) string {
	sorted := append([]string(nil), effective...)
	sort.Strings(sorted)
	rendered := "[" + strings.Join(quoteAll(sorted), " ") + "]"
	if target == "" {
		return fmt.Sprintf("OnTarget %s not satisfied; no target selected", rendered)
	}
	return fmt.Sprintf("OnTarget %s does not include active target %q", rendered, target)
}

func quoteAll(in []string) []string {
	out := make([]string, len(in))
	for i, s := range in {
		out[i] = fmt.Sprintf("%q", s)
	}
	return out
}

// validateOnTargetSelection was the v0.5 gate that rejected
// OnTarget declarations referring to undeclared targets. In v0.6
// pipeline YAML no longer carries a targets block, so OnTarget
// declarations always pass this gate -- the runtime simply skips
// every OnTarget-declaring step because no target is ever set. See
// docs/migrations/v0.6.0.md for the recommended replacement
// (one-pipeline-per-target).
func validateOnTargetSelection(opts Options, plan *sparkwing.Plan) error {
	_ = opts
	_ = plan
	return nil
}

func checkJobOnTarget(pipelineName string, n *sparkwing.JobNode, declared map[string]struct{}, hasTargets bool, yaml *pipelines.Pipeline) error {
	list := n.OnTargets()
	if len(list) == 0 {
		return nil
	}
	if yaml == nil {
		return nil
	}
	if !hasTargets {
		return fmt.Errorf("pipeline %q: job %q has OnTarget but pipeline declares no targets; declare a targets block or remove OnTarget",
			pipelineName, n.ID())
	}
	for _, t := range list {
		if _, ok := declared[t]; !ok {
			return fmt.Errorf("pipeline %q: job %q OnTarget(%q) refers to undeclared target; declared: %v",
				pipelineName, n.ID(), t, sortedDeclaredTargets(declared))
		}
	}
	return nil
}

func checkStepOnTarget(pipelineName, jobID string, s *sparkwing.WorkStep, declared map[string]struct{}, hasTargets bool, yaml *pipelines.Pipeline) error {
	list := s.OnTargets()
	if len(list) == 0 {
		return nil
	}
	if yaml == nil {
		return nil
	}
	if !hasTargets {
		return fmt.Errorf("pipeline %q: job %q step %q has OnTarget but pipeline declares no targets; declare a targets block or remove OnTarget",
			pipelineName, jobID, s.ID())
	}
	for _, t := range list {
		if _, ok := declared[t]; !ok {
			return fmt.Errorf("pipeline %q: job %q step %q OnTarget(%q) refers to undeclared target; declared: %v",
				pipelineName, jobID, s.ID(), t, sortedDeclaredTargets(declared))
		}
	}
	return nil
}

func sortedDeclaredTargets(m map[string]struct{}) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}
