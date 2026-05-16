package sparkwing

// RequiresProvider is optionally implemented by a Workable struct to
// declare its own hard runner constraint. The orchestrator reads it
// once when wrapping the Workable into a *JobNode and seeds the
// node's Requires list with the returned terms. Chainable .Requires
// calls overwrite the seeded value, matching the existing "no-arg
// clears" semantics of *JobNode.Requires.
//
// Useful when the constraint is intrinsic to the work (Windows-only
// build, USB-attached hardware) rather than per-call site, and for
// fan-out generators where each generated instance needs its own
// labels based on its data:
//
//	type BenchShard struct{ Spec ShardSpec }
//	func (b BenchShard) Run(ctx context.Context) error { /* ... */ }
//	func (b BenchShard) Requires() []string {
//	    if b.Spec.NeedsUSB {
//	        return []string{"local"}
//	    }
//	    return []string{"cloud-linux"}
//	}
type RequiresProvider interface {
	Requires() []string
}

// PrefersProvider is optionally implemented by a Workable struct to
// declare its own runner preferences. Same precedence as
// RequiresProvider: seeds *JobNode.Prefers at construction; the
// chainable .Prefers call overwrites.
type PrefersProvider interface {
	Prefers() []string
}

// WhenRunnerProvider is optionally implemented by a Workable struct
// to declare its own dispatch-time eligibility labels. Same
// precedence as RequiresProvider: seeds *JobNode.WhenRunner at
// construction; the chainable .WhenRunner call overwrites.
type WhenRunnerProvider interface {
	WhenRunner() []string
}

// applyWorkableLabels reads any provider interface a Workable
// implements and seeds the corresponding *JobNode label set. Empty
// slices from a provider clear the field (consistent with the
// chainable verb's no-arg behavior); a Workable that does not
// implement a given interface leaves the field untouched.
//
// Called from newNode so every construction path (the Job verb, the
// fan-out generators, OnFailure recovery nodes, and spawn-handler
// detached nodes) picks up Workable-declared labels uniformly.
func applyWorkableLabels(n *JobNode, w Workable) {
	if n == nil || w == nil {
		return
	}
	if rp, ok := w.(RequiresProvider); ok {
		n.requires = normalizeLabels(rp.Requires())
	}
	if pp, ok := w.(PrefersProvider); ok {
		n.prefers = normalizeLabels(pp.Prefers())
	}
	if wp, ok := w.(WhenRunnerProvider); ok {
		n.whenRunner = normalizeLabels(wp.WhenRunner())
	}
}
