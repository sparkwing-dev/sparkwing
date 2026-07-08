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
	if plan, ok := bestEffortPlan(reg); ok {
		if union, perStep := collectRisksFromPlan(plan); len(union) > 0 || len(perStep) > 0 {
			if len(union) > 0 {
				dp.Risks = union
			}
			if len(perStep) > 0 {
				dp.RisksBySteps = perStep
			}
		}
		dp.Args = appendTransitiveArgs(dp.Args, plan)
	}
	return dp, true, nil
}

// appendTransitiveArgs walks the plan's WithArgs[T] schemas and
// appends one DescribeArg per declared flag. Flags already present
// in args (pipeline-level Inputs) are skipped on name match so a
// pipeline that re-declares a job-owned arg at the Inputs level
// stays the authoritative entry. JobID is stamped from the
// transitive surface so the help renderer can show "[from <job>]".
func appendTransitiveArgs(args []sparkwing.DescribeArg, plan *sparkwing.Plan) []sparkwing.DescribeArg {
	surface := plan.TransitiveArgsSurface()
	if len(surface) == 0 {
		return args
	}
	have := make(map[string]bool, len(args))
	for _, a := range args {
		have[a.Name] = true
	}
	jobsByFlag := make(map[string]string, len(surface))
	schemasSeen := map[*sparkwing.Schema]string{}
	for flag, t := range surface {
		jobsByFlag[flag] = t.JobID
		schemasSeen[t.Schema] = t.JobID
	}
	for s, jobID := range schemasSeen {
		for _, da := range s.DescribeArgs() {
			if have[da.Name] {
				continue
			}
			da.JobID = jobID
			args = append(args, da)
			have[da.Name] = true
		}
	}
	return args
}

// bestEffortPlan best-effort invokes the pipeline's Plan() with an
// empty args map so describe-time consumers (risk labels, transitive
// args) can walk the DAG. Failures (panics, required-Inputs errors)
// are swallowed -- the describe cache degrades gracefully so an
// older or required-flag pipeline doesn't break --help.
func bestEffortPlan(reg *sparkwing.Registration) (plan *sparkwing.Plan, ok bool) {
	if reg == nil || reg.Invoke == nil {
		return nil, false
	}
	defer func() {
		if r := recover(); r != nil {
			plan, ok = nil, false
		}
	}()
	p, err := reg.Invoke(sparkwing.SkipArgResolve(context.Background()), nil, sparkwing.RunContext{Pipeline: reg.Name})
	if err != nil || p == nil {
		return nil, false
	}
	return p, true
}

// collectRisksFromPlan returns the union + per-step breakdown of
// declared risk labels reachable in plan. Caller already obtained
// plan via bestEffortPlan so failures upstream short-circuit out.
func collectRisksFromPlan(plan *sparkwing.Plan) (union []string, perStep []sparkwing.DescribeStepRisks) {
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
	return union, perStep
}
