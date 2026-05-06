package sparkwing

import (
	"context"
	"fmt"
)

// PlanPreview is the runtime-resolved view of a Plan: the same DAG
// `pipeline explain` shows, plus per-step "would run / would skip
// <reason>" annotations evaluated against the supplied args +
// --start-at / --stop-at bounds. IMP-013: this is the structured
// "what would happen if you ran this" object agents inspect before
// destructive operations -- terraform plan for sparkwing.
//
// The wire shape is intentionally close to the existing plan-explain
// snapshot so renderers can layer the runtime decisions onto the
// static structure without re-parsing.
type PlanPreview struct {
	Pipeline string `json:"pipeline"`
	// Venue is the pipeline's author-declared dispatch constraint
	// ("either" / "local-only" / "cluster-only"). IMP-011: pre-flight
	// consumers see venue alongside the DAG so a `pipeline plan`
	// renderer can warn an operator that this pipeline can't be
	// `--on`'d without going through the dispatcher first.
	Venue string `json:"venue,omitempty"`
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
	// of the apply Fn (IMP-014).
	Decision string `json:"decision"`
	// SkipReason categorizes a "would_skip":
	//   - "user_skipif"        : a SkipIf predicate matched
	//   - "range_skip"         : outside --start-at..--stop-at window
	//   - "no_dry_run_defined" : --dry-run + no DryRunFn + no
	//                            SafeWithoutDryRun marker (IMP-014)
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
	// BlastRadius is the author-declared marker set on this step
	// (IMP-015): "destructive" / "production" / "money". Empty when
	// no marker was declared. Surfaced on PreviewItem so
	// `pipeline plan` consumers and agents see the contract
	// alongside the runtime decision rather than fetching it from
	// a separate describe round-trip.
	BlastRadius []string `json:"blast_radius,omitempty"`
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
	// gap is visible in the preview output. IMP-014.
	DryRun bool
}

// PreviewPlan walks an already-built Plan and returns the runtime-
// resolved view. The Plan must come from Registration.Invoke(args)
// so IMP-008's Plan-time validation has already run; PreviewPlan
// itself does NOT execute step bodies. SkipIf predicates are
// evaluated against a synthetic plan-only ctx (the same one Plan()
// itself runs under, so the SDK-012 guard catches any side-effect
// helper a predicate accidentally invokes).
//
// Caller threads the wire-format args map back through
// ResolvedArgs so renderers can show the operator the inputs that
// drove the decisions -- single source of truth for what was
// actually parsed.
func PreviewPlan(plan *Plan, pipeline string, resolvedArgs map[string]string, opts PreviewOptions) (*PlanPreview, error) {
	if plan == nil {
		return nil, fmt.Errorf("PreviewPlan: plan is nil")
	}
	out := &PlanPreview{
		Pipeline:     pipeline,
		ResolvedArgs: resolvedArgs,
		StartAt:      opts.StartAt,
		StopAt:       opts.StopAt,
	}
	// IMP-011: surface the registered venue as plan-level metadata so
	// `sparkwing pipeline plan` consumers can render the dispatch
	// constraint above the DAG. Lookup is best-effort -- a Plan built
	// against an unregistered name (synthetic test fixtures) just
	// omits the field.
	if reg, ok := Lookup(pipeline); ok {
		out.Venue = PipelineVenue(reg).String()
	}
	for _, lw := range plan.LintWarnings() {
		out.LintWarnings = append(out.LintWarnings, PreviewLintWarning{
			NodeID:  lw.NodeID,
			Message: lw.Msg,
		})
	}

	// Plan-only ctx so SkipIf predicates that touch SDK-012-guarded
	// helpers panic with the canonical "Plan() must be pure-
	// declarative" message rather than silently shelling out.
	planCtx := withPlanTime(context.Background())

	// Dedupe recovery nodes that are also plan.Add'd directly --
	// mirrors the orchestrator's marshalPlanSnapshot walk so the
	// preview wire shape matches the explain snapshot.
	seen := make(map[string]bool)
	for _, n := range plan.Nodes() {
		out.Nodes = append(out.Nodes, previewNode(planCtx, n, "", opts))
		seen[n.ID()] = true
	}
	// IMP-029: surface .OnFailure(id, job) recovery nodes. They're
	// constructed detached and live on the parent's onFailure pointer,
	// so plan.Nodes() doesn't return them. Emit a PreviewNode for each
	// unseen recovery with OnFailureOf pointing back to the parent so
	// `pipeline plan` can render the failure-branch attachment without
	// re-parsing the snapshot.
	for _, n := range plan.Nodes() {
		recID := n.OnFailureNodeID()
		if recID == "" || seen[recID] {
			continue
		}
		rec := n.OnFailureNode()
		if rec == nil {
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
// recovery target; "" for ordinary plan nodes. IMP-029: PreviewPlan
// threads this in from its own walk rather than reaching into n,
// matching how marshalPlanSnapshot encodes recovery nodes.
func previewNode(ctx context.Context, n *Node, onFailureOf string, opts PreviewOptions) PreviewNode {
	pn := PreviewNode{
		ID:          n.ID(),
		Deps:        append([]string(nil), n.DepIDs()...),
		OnFailureOf: onFailureOf,
		Decision:    "would_run",
	}
	if n.approval != nil {
		pn.IsApproval = true
		return pn
	}

	w := n.Work()
	if w == nil {
		return pn
	}
	pw := previewWork(ctx, w, opts)
	pn.Work = pw

	// Roll up the node-level decision: "would_skip" only when every
	// visible item is itself skipped. Otherwise the node still
	// dispatches (even if some inner steps no-op individually).
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
func previewWork(ctx context.Context, w *Work, opts PreviewOptions) *PreviewWork {
	rangeSkips := w.PreviewSkipForRange(opts.StartAt, opts.StopAt)

	pw := &PreviewWork{}
	for _, s := range w.Steps() {
		item := previewItem(ctx, s.ID(), s.DepIDs(), rangeSkips, s.SkipPredicates())
		// IMP-015: surface the author-declared blast-radius set on
		// the preview item so agents reading `pipeline plan --json`
		// see the contract alongside the runtime decision. Stringify
		// at the wire layer so JSON consumers don't need the typed
		// constant set.
		if br := s.BlastRadius(); len(br) > 0 {
			strs := make([]string, len(br))
			for i, m := range br {
				strs[i] = m.String()
			}
			item.BlastRadius = strs
		}
		// IMP-014: refine the per-step decision through the dry-run
		// lens AFTER the skip precedence (range / user-skipif) is
		// computed -- a step that's already going to be skipped
		// keeps that reason regardless of dry-run mode.
		if opts.DryRun && item.Decision == "would_run" {
			switch {
			case s.HasDryRun():
				item.Decision = "would_dry_run"
			case s.IsSafeWithoutDryRun():
				// keep "would_run" -- author marked the apply Fn
				// read-only, so dispatch under --dry-run runs it
				// unmodified.
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
		// SpawnNodeForEach generators are scheduled like steps but
		// fan out at runtime to N children whose count depends on
		// the upstream item's typed output. At plan time we don't
		// have that output; surface "unresolved" + the source-id
		// hint so renderers can be honest about the limit.
		item := previewItem(ctx, g.ID(), g.DepIDs(), rangeSkips, nil)
		item.Cardinality = "unresolved"
		if deps := g.DepIDs(); len(deps) > 0 {
			// First dep is conventionally the source whose output
			// drives the fan-out; renderers can show all deps in
			// the Needs list separately. This is best-effort -- the
			// SDK doesn't enforce "first dep == source" today.
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
func previewItem(ctx context.Context, id string, needs []string, rangeSkips map[string]string, predicates []SkipPredicate) PreviewItem {
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
			// Predicate panicked under the plan-only ctx (likely an
			// SDK-012 guard fired on a side-effect helper). Mark the
			// item as "would_skip user_skipif" with the panic
			// message attached so the operator sees the contract
			// violation rather than guessing.
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
func safeEvalPredicate(ctx context.Context, p SkipPredicate) (match bool, panicMsg string) {
	defer func() {
		if r := recover(); r != nil {
			panicMsg = fmt.Sprintf("%v", r)
		}
	}()
	return p(ctx), ""
}
