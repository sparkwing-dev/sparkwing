package sparkwingruntime

import (
	"context"
	"fmt"

	"github.com/sparkwing-dev/sparkwing/sparkwing"
	"github.com/sparkwing-dev/sparkwing/sparkwing/planguard"
)

type PlanPreview struct {
	Pipeline string `json:"pipeline"`

	ResolvedArgs map[string]string `json:"resolved_args,omitempty"`

	StartAt string `json:"start_at,omitempty"`
	StopAt  string `json:"stop_at,omitempty"`

	LintWarnings []PreviewLintWarning `json:"lint_warnings,omitempty"`

	Nodes []PreviewNode `json:"nodes"`
}

type PreviewLintWarning struct {
	NodeID  string `json:"node_id,omitempty"`
	Message string `json:"message"`
}

type PreviewNode struct {
	ID          string   `json:"id"`
	Deps        []string `json:"deps,omitempty"`
	IsApproval  bool     `json:"is_approval,omitempty"`
	OnFailureOf string   `json:"on_failure_of,omitempty"`

	Decision   string `json:"decision"`
	SkipReason string `json:"skip_reason,omitempty"`

	Work *PreviewWork `json:"work,omitempty"`
}

type PreviewWork struct {
	Steps     []PreviewItem `json:"steps,omitempty"`
	Spawns    []PreviewItem `json:"spawns,omitempty"`
	SpawnEach []PreviewItem `json:"spawn_each,omitempty"`
}

type PreviewItem struct {
	ID    string   `json:"id"`
	Needs []string `json:"needs,omitempty"`

	Decision string `json:"decision"`

	SkipReason string `json:"skip_reason,omitempty"`

	SkipDetail string `json:"skip_detail,omitempty"`

	Cardinality string `json:"cardinality,omitempty"`

	CardinalitySource string `json:"cardinality_source,omitempty"`

	Risks []string `json:"risks,omitempty"`
}

type PreviewOptions struct {
	StartAt string
	StopAt  string

	DryRun bool
}

func PreviewPlan(plan *sparkwing.Plan, pipeline string, resolvedArgs map[string]string, opts PreviewOptions) (*PlanPreview, error) {
	if plan == nil {
		return nil, fmt.Errorf("PreviewPlan: plan is nil")
	}
	if err := ValidateStepRange(plan, opts.StartAt, opts.StopAt); err != nil {
		return nil, err
	}
	out := &PlanPreview{
		Pipeline:     pipeline,
		ResolvedArgs: resolvedArgs,
		StartAt:      opts.StartAt,
		StopAt:       opts.StopAt,
	}
	for _, lw := range plan.LintWarnings() {
		out.LintWarnings = append(out.LintWarnings, PreviewLintWarning{
			NodeID:  lw.NodeID,
			Message: lw.Msg,
		})
	}

	planCtx := planguard.With(context.Background())

	seen := make(map[string]bool)
	for _, n := range plan.Nodes() {
		out.Nodes = append(out.Nodes, previewNode(planCtx, n, "", opts))
		seen[n.ID()] = true
	}
	for _, n := range plan.Nodes() {
		rec := n.OnFailureNode()
		if rec == nil {
			continue
		}
		recID := rec.ID()
		if seen[recID] {
			continue
		}
		out.Nodes = append(out.Nodes, previewNode(planCtx, rec, n.ID(), opts))
		seen[recID] = true
	}
	return out, nil
}

func previewNode(ctx context.Context, n *sparkwing.JobNode, onFailureOf string, opts PreviewOptions) PreviewNode {
	pn := PreviewNode{
		ID:          n.ID(),
		Deps:        append([]string(nil), n.DepIDs()...),
		OnFailureOf: onFailureOf,
		Decision:    "would_run",
	}
	if n.IsApproval() {
		pn.IsApproval = true
		return pn
	}

	w := n.Work()
	if w == nil {
		return pn
	}
	pw := previewWork(ctx, w, opts)
	pn.Work = pw

	allSkipped := true
	hasVisible := false
	for _, items := range [][]PreviewItem{pw.Steps, pw.Spawns, pw.SpawnEach} {
		for _, it := range items {
			hasVisible = true
			if it.Decision != "would_skip" {
				allSkipped = false
			}
		}
	}
	if hasVisible && allSkipped {
		pn.Decision = "would_skip"
		pn.SkipReason = "all_steps_skipped"
	}
	return pn
}

func previewWork(ctx context.Context, w *sparkwing.Work, opts PreviewOptions) *PreviewWork {
	rangeSkips := w.PreviewSkipForRange(opts.StartAt, opts.StopAt)

	pw := &PreviewWork{}
	for _, s := range w.Steps() {
		item := previewItem(ctx, s.ID(), s.DepIDs(), rangeSkips, s.SkipPredicates())
		if risks := s.Risks(); len(risks) > 0 {
			item.Risks = risks
		}
		if opts.DryRun && item.Decision == "would_run" {
			switch {
			case s.HasDryRun():
				item.Decision = "would_dry_run"
			case s.IsSafeWithoutDryRun():
			default:
				item.Decision = "would_skip"
				item.SkipReason = "no_dry_run_defined"
			}
		}
		pw.Steps = append(pw.Steps, item)
	}
	for _, sp := range w.Spawns() {
		pw.Spawns = append(pw.Spawns, previewItem(ctx, sp.ID(), sp.DepIDs(), rangeSkips, sp.SkipPredicates()))
	}
	for _, g := range w.SpawnGens() {
		item := previewItem(ctx, g.ID(), g.DepIDs(), rangeSkips, nil)
		item.Cardinality = "unresolved"
		if deps := g.DepIDs(); len(deps) > 0 {
			item.CardinalitySource = deps[0]
		}
		pw.SpawnEach = append(pw.SpawnEach, item)
	}
	return pw
}

func previewItem(ctx context.Context, id string, needs []string, rangeSkips map[string]string, predicates []sparkwing.SkipPredicate) PreviewItem {
	item := PreviewItem{
		ID:       id,
		Needs:    append([]string(nil), needs...),
		Decision: "would_run",
	}
	if reason, ok := rangeSkips[id]; ok {
		item.Decision = "would_skip"
		item.SkipReason = "range_skip"
		item.SkipDetail = reason
		return item
	}
	for _, p := range predicates {
		if p == nil {
			continue
		}
		match, panicMsg := safeEvalPredicate(ctx, p)
		if panicMsg != "" {
			item.Decision = "would_skip"
			item.SkipReason = "user_skipif"
			item.SkipDetail = "predicate panicked at plan time: " + panicMsg
			return item
		}
		if match {
			item.Decision = "would_skip"
			item.SkipReason = "user_skipif"
			return item
		}
	}
	return item
}

func safeEvalPredicate(ctx context.Context, p sparkwing.SkipPredicate) (match bool, panicMsg string) {
	defer func() {
		if r := recover(); r != nil {
			panicMsg = fmt.Sprintf("%v", r)
		}
	}()
	return p(ctx), ""
}
