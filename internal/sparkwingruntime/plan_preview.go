package sparkwingruntime

import (
	"context"
	"fmt"

	"github.com/sparkwing-dev/sparkwing/sparkwing"
	"github.com/sparkwing-dev/sparkwing/sparkwing/planguard"
)

// PlanPreview is the runtime-resolved view of a Plan: the same DAG
// `pipeline explain` shows, plus per-step "would run / would skip
// <reason>" annotations evaluated against the supplied args +
// --start-at / --stop-at bounds. This is the structured
// "what would happen if you ran this" object agents inspect before
// destructive operations -- terraform plan for sparkwing.
//
// The wire shape is intentionally close to the existing plan-explain
// snapshot so renderers can layer the runtime decisions onto the
// static structure without re-parsing.
type PlanPreview struct {
	Pipeline string `json:"pipeline"`
	// ResolvedArgs are the typed Inputs values after default
	// resolution + flag parsing. Plain map for JSON-friendliness.
	ResolvedArgs map[string]string `json:"resolved_args,omitempty"`
	// StartAt / StopAt echo the bounds the preview was computed
	// against, so JSON consumers don't have to thread state.
	StartAt string `json:"start_at,omitempty"`
	StopAt  string `json:"stop_at,omitempty"`
	// LintWarnings are the Plan-time advisories accumulated by the
	// SDK during Plan() (already exposed via plan.LintWarnings()).
	LintWarnings []PreviewLintWarning `json:"lint_warnings,omitempty"`
	// Nodes is the per-Plan-node breakdown, in topological order.
	Nodes []PreviewNode `json:"nodes"`
}

// PreviewLintWarning mirrors LintWarning for JSON emission.
type PreviewLintWarning struct {
	NodeID  string `json:"node_id,omitempty"`
	Message string `json:"message"`
}

// PreviewNode is one Plan node + its inner Work, with each Work
// item annotated with the runtime decision the orchestrator would
// reach.
type PreviewNode struct {
	ID          string   `json:"id"`
	Deps        []string `json:"deps,omitempty"`
	IsApproval  bool     `json:"is_approval,omitempty"`
	OnFailureOf string   `json:"on_failure_of,omitempty"`
	// Decision is "would_run" or "would_skip"; SkipReason
	// classifies skipped nodes ("user_skipif" / "range_skip" /
	// "synthetic"). Computed by walking the inner Work's items and
	// rolling up: a node "would_skip" only when EVERY non-hidden
	// item in its Work is itself skipped (otherwise the node still
	// dispatches even if some inner steps no-op).
	Decision   string `json:"decision"`
	SkipReason string `json:"skip_reason,omitempty"`
	// Work is the per-item breakdown. Empty for Approval gates.
	Work *PreviewWork `json:"work,omitempty"`
}

// PreviewWork is the inner-Work view: each Step / SpawnNode /
// SpawnNodeForEach with its runtime decision.
type PreviewWork struct {
	Steps     []PreviewItem `json:"steps,omitempty"`
	Spawns    []PreviewItem `json:"spawns,omitempty"`
	SpawnEach []PreviewItem `json:"spawn_each,omitempty"`
}

// PreviewItem is one Work item + decision. Cardinality is filled
// only for SpawnNodeForEach generators -- the canonical "unresolved
// at plan time" sentinel for dynamic fan-out.
type PreviewItem struct {
	ID    string   `json:"id"`
	Needs []string `json:"needs,omitempty"`
	// Decision: "would_run" | "would_dry_run" | "would_skip".
	// "would_dry_run" only appears under PreviewOptions.DryRun and
	// indicates the step has a DryRunFn that would execute in place
	// of the apply Fn.
	Decision string `json:"decision"`
	// SkipReason categorizes a "would_skip":
	//   - "user_skipif"        : a SkipIf predicate matched
	//   - "range_skip"         : outside --start-at..--stop-at window
	//   - "no_dry_run_defined" : --dry-run + no DryRunFn + no
	//                            SafeWithoutDryRun marker
	//   - "synthetic"          : hidden generator items
	// Empty for "would_run" / "would_dry_run".
	SkipReason string `json:"skip_reason,omitempty"`
	// SkipDetail is human text accompanying the reason: the
	// stringified bound for range_skip, or the panic message if a
	// SkipIf predicate panicked under the plan-only ctx.
	SkipDetail string `json:"skip_detail,omitempty"`
	// Cardinality is "unresolved" for SpawnNodeForEach generators
	// (the cardinality depends on a runtime value); empty for
	// non-generator items.
	Cardinality string `json:"cardinality,omitempty"`
	// CardinalitySource names the item whose runtime output
	// determines the count, when applicable.
	CardinalitySource string `json:"cardinality_source,omitempty"`
	// Risks is the author-declared risk-label set on this step.
	// Empty when no label was declared. Surfaced on PreviewItem so
	// `pipeline plan` consumers and agents see the contract
	// alongside the runtime decision rather than fetching it from
	// a separate describe round-trip.
	Risks []string `json:"risks,omitempty"`
}

// PreviewOptions carries the operator-supplied state the plan walk
// needs: --start-at / --stop-at bounds, plus a hook for future
// fields (resolved profile, RunContext shape, etc.) without
// breaking callers.
type PreviewOptions struct {
	StartAt string
	StopAt  string
	// DryRun routes the per-step decision through the dry-run lens:
	// steps with a DryRunFn render as "would_dry_run", steps marked
	// SafeWithoutDryRun keep "would_run" (their apply body is
	// read-only by author contract), and steps with neither become
	// "would_skip" + reason "no_dry_run_defined" so the contract
	// gap is visible in the preview output.
	DryRun bool
}

// PreviewPlan walks an already-built Plan and returns the runtime-
// resolved view. The Plan must come from Registration.Invoke(args)
// so Plan-time ref validation has already run; PreviewPlan itself
// does NOT execute step bodies. SkipIf predicates are evaluated
// against a synthetic plan-only ctx (the same one Plan() itself
// runs under, so the side-effect guard catches any helper a
// predicate accidentally invokes).
//
// Caller threads the wire-format args map back through
// ResolvedArgs so renderers can show the operator the inputs that
// drove the decisions -- single source of truth for what was
// actually parsed.
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

// previewNode renders one Plan node + its inner Work. Approval
// gates have no Work; their decision is always "would_run" (the
// gate always fires; the human's response is the runtime input
// that's outside plan-time visibility).
//
// onFailureOf is the parent ID when n is a .OnFailure(id, job)
// recovery target; "" for ordinary plan nodes. PreviewPlan threads
// this in from its own walk rather than reaching into n, matching
// how marshalPlanSnapshot encodes recovery nodes.
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

// previewWork iterates Steps / Spawns / SpawnGens and computes the
// per-item decision: range-skip wins (matches RunWork's runtime
// precedence so plan output and execution agree), then SkipIf, then
// "would_run".
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

// previewItem applies the same skip precedence RunWork uses:
// range-skip first, then user SkipIf predicates, then "would_run".
// SkipPredicates are called with a plan-only ctx; a panic in user
// code is caught and surfaced on the item's SkipDetail rather than
// crashing the whole plan command.
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

// safeEvalPredicate calls p(ctx) with a panic recover, returning
// the boolean result + a non-empty panic message when the predicate
// crashed. Lets PreviewPlan stay best-effort: a malformed predicate
// in one step doesn't blow up the entire plan render.
func safeEvalPredicate(ctx context.Context, p sparkwing.SkipPredicate) (match bool, panicMsg string) {
	defer func() {
		if r := recover(); r != nil {
			panicMsg = fmt.Sprintf("%v", r)
		}
	}()
	return p(ctx), ""
}
