package sparkwingruntime

import (
	"context"
	"fmt"
	"strings"

	"github.com/sparkwing-dev/sparkwing/sparkwing"
)

// DescribeAll returns the schema for every registered pipeline.
func DescribeAll() ([]sparkwing.DescribePipeline, error) {
	names := sparkwing.Registered()
	out := make([]sparkwing.DescribePipeline, 0, len(names))
	for _, n := range names {
		dp, ok, err := DescribePipelineByName(n)
		if err != nil {
			return nil, fmt.Errorf("describe %q: %w", n, err)
		}
		if !ok {
			continue
		}
		out = append(out, dp)
	}
	return out, nil
}

// DescribePipelineByName returns the schema for one registered
// pipeline.
func DescribePipelineByName(name string) (sparkwing.DescribePipeline, bool, error) {
	reg, ok := sparkwing.Lookup(name)
	if !ok {
		return sparkwing.DescribePipeline{}, false, nil
	}
	dp := sparkwing.DescribePipeline{
		Name:  reg.Name,
		Args:  []sparkwing.DescribeArg{},
		Extra: reg.Schema.Extra,
	}
	if inst := reg.Instance(); inst != nil {
		if s, ok := inst.(sparkwing.ShortHelpProvider); ok {
			dp.Short = strings.TrimSpace(s.ShortHelp())
		}
		if h, ok := inst.(sparkwing.HelpProvider); ok {
			dp.Help = strings.TrimSpace(h.Help())
		}
		if e, ok := inst.(sparkwing.ExampleProvider); ok {
			dp.Examples = e.Examples()
		}
		if ev, ok := inst.(sparkwing.EnvVarDocer); ok {
			dp.EnvVars = ev.EnvVars()
		}
	}
	for _, f := range reg.Schema.Fields {
		if f.IsExtraBag() {
			continue
		}
		dp.Args = append(dp.Args, sparkwing.DescribeArg{
			Name:     f.Name,
			GoName:   f.GoName,
			Short:    f.Short,
			Type:     f.Type,
			Required: f.Required,
			Desc:     f.Description,
			Default:  f.Default,
			Enum:     f.Enum,
			Secret:   f.Secret,
		})
	}
	// Best-effort risk-label union + per-step breakdown.
	// We invoke Plan() with empty args to walk the DAG; pipelines
	// with required Inputs (or that panic at Plan-time without args)
	// gracefully degrade to empty labels. The sparkwing dispatcher
	// treats absent labels as "no gate fires" so a pipeline that
	// can't be described stays dispatchable -- the next manual run
	// will enforce the gate via the actual Plan walk.
	if union, perStep, ok := collectRisks(reg); ok {
		if len(union) > 0 {
			dp.Risks = union
		}
		if len(perStep) > 0 {
			dp.RisksBySteps = perStep
		}
	}
	return dp, true, nil
}

// collectRisks best-effort invokes the pipeline's Plan() with an
// empty args map, walks every reachable WorkStep, and returns the
// sorted union of declared risk labels plus the per-step breakdown.
// Failures (panics, required-Inputs errors) are swallowed so an
// older or required-flag pipeline doesn't break --describe -- the
// dispatcher's gate degrades gracefully when labels are absent.
func collectRisks(reg *sparkwing.Registration) (union []string, perStep []sparkwing.DescribeStepRisks, ok bool) {
	if reg == nil || reg.Invoke == nil {
		return nil, nil, false
	}
	defer func() {
		if r := recover(); r != nil {
			// A pipeline that panics under empty args (e.g. asserts a
			// required input is non-empty inside Plan) is allowed --
			// the label walk is best-effort, and the dispatcher's
			// "no labels detected" path keeps that dispatch safe-
			// by-default rather than blocked-by-default.
			union, perStep, ok = nil, nil, false
		}
	}()
	plan, err := reg.Invoke(context.Background(), nil, sparkwing.RunContext{Pipeline: reg.Name})
	if err != nil || plan == nil {
		return nil, nil, false
	}
	var perStepLabels [][]string
	for _, n := range plan.Nodes() {
		w := n.Work()
		if w == nil {
			continue
		}
		for _, s := range w.Steps() {
			labels := s.Risks()
			if len(labels) == 0 {
				continue
			}
			perStep = append(perStep, sparkwing.DescribeStepRisks{
				NodeID: n.ID(),
				StepID: s.ID(),
				Labels: labels,
			})
			perStepLabels = append(perStepLabels, labels)
		}
	}
	union = SortedUniqueRisks(perStepLabels...)
	return union, perStep, true
}
