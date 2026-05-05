package sparkwing

// Outcome is the terminal state of a node in a Plan run.
//
// Success and Failed are returned by the job itself. Satisfied is also
// set by the job to report that no work was needed (reason required).
// Cached, Skipped, and Cancelled are set by the orchestrator without
// the job ever executing.
type Outcome string

const (
	Success   Outcome = "success"
	Failed    Outcome = "failed"
	Satisfied Outcome = "satisfied"
	Cached    Outcome = "cached"
	Skipped   Outcome = "skipped"
	Cancelled Outcome = "cancelled"

	// SkippedConcurrent: .Cache() arrival hit a full slot under
	// OnLimit:Skip. Distinct from Skipped (which comes from SkipIf)
	// so dashboards can surface the cause.
	SkippedConcurrent Outcome = "skipped-concurrent"

	// Superseded: .Cache() holder was evicted by a newer arrival under
	// OnLimit:CancelOthers. Distinct from Cancelled (operator-driven)
	// so dashboards can surface "evicted by newer run" vs "operator
	// cancelled".
	Superseded Outcome = "superseded"
)

// Terminal reports whether the outcome ends the node's lifecycle.
// All defined outcomes are terminal; this exists for symmetry with
// future transient states (e.g. running, pending).
func (o Outcome) Terminal() bool { return true }

// Paused is the non-terminal status used for the debug pause surface.
// Kept as a plain string constant rather than an Outcome value so the
// Outcome enum's "terminal" contract stays intact.
const Paused = "paused"

// OK reports whether the outcome satisfies downstream dependencies.
// Success, Satisfied, Cached, and Skipped all let downstream proceed —
// Skipped is distinct from Failed (the node never ran for a reasoned
// decision, not because of a fault). Failed and Cancelled do not
// satisfy downstream.
func (o Outcome) OK() bool {
	switch o {
	case Success, Satisfied, Cached, Skipped, SkippedConcurrent:
		return true
	}
	return false
}
