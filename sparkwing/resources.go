package sparkwing

import (
	"fmt"
	"math"
)

// ResourceHints is the resolved set of cold-start cost hints declared
// via [Plan.Resources] or [JobNode.Resources]. Hints are optional
// advisory estimates of peak usage that admission uses before any
// measured profile exists for the pipeline; absent hints mean the
// admission layer falls back to conservative defaults until it has
// measured real runs. A hint is never a limit -- work that exceeds its
// hint is not throttled or killed.
type ResourceHints struct {
	// Cores is the estimated peak number of CPU cores the work uses.
	// Fractional values are meaningful (0.5 = half a core). Zero means
	// no hint was given for cores.
	Cores float64
	// MemoryBytes is the estimated peak resident memory in bytes.
	// Authors express this via [MemoryGB]; it is stored in bytes. Zero
	// means no hint was given for memory.
	MemoryBytes int64
}

// ResourceHint is one option for [Plan.Resources] or
// [JobNode.Resources]. Construct via [Cores] or [MemoryGB].
type ResourceHint func(*ResourceHints)

// Cores hints that the work peaks at n CPU cores. Fractional values
// are meaningful: Cores(0.5) declares light, mostly-waiting work.
// Panics unless n is a positive, finite number.
func Cores(n float64) ResourceHint {
	if n <= 0 || math.IsInf(n, 0) || math.IsNaN(n) {
		panic(fmt.Sprintf("sparkwing: Cores(%v): hint must be a positive, finite number of cores", n))
	}
	return func(h *ResourceHints) { h.Cores = n }
}

// MemoryGB hints that the work peaks at n gigabytes of resident
// memory, where one gigabyte is 2^30 bytes. The value is stored in
// bytes (see [ResourceHints.MemoryBytes]). Panics unless n is a
// positive, finite number.
func MemoryGB(n float64) ResourceHint {
	if n <= 0 || math.IsInf(n, 0) || math.IsNaN(n) {
		panic(fmt.Sprintf("sparkwing: MemoryGB(%v): hint must be a positive, finite number of gigabytes", n))
	}
	return func(h *ResourceHints) { h.MemoryBytes = int64(math.Round(n * float64(1<<30))) }
}

// Resources declares optional cold-start cost hints for the whole run:
// the estimated peak CPU and memory the run occupies on its host while
// admission has no measured profile for the pipeline yet. Hints are
// advisory -- they inform how much host budget admission reserves
// before dispatch, they never cap or kill the work.
//
//	func (p *Deploy) Plan(ctx context.Context, plan *sparkwing.Plan, in Inputs, rc sparkwing.RunContext) error {
//	    plan.Resources(sparkwing.Cores(4), sparkwing.MemoryGB(8))
//	    ...
//	}
//
// Repeated calls merge: each hint overwrites the same dimension set by
// an earlier call and leaves the other dimensions intact. Calling with
// no hints clears every plan-level hint.
func (p *Plan) Resources(hints ...ResourceHint) *Plan {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.resources = applyResourceHints(p.resources, hints)
	return p
}

// ResourceHints returns a copy of the plan-level cost hints declared
// via [Plan.Resources], or nil when the plan declared none.
func (p *Plan) ResourceHints() *ResourceHints {
	p.mu.Lock()
	defer p.mu.Unlock()
	return copyResourceHints(p.resources)
}

// Resources declares optional cold-start cost hints for this node: the
// estimated peak CPU and memory the node's work occupies while it
// runs. Same semantics as [Plan.Resources], scoped to one node --
// hints are advisory admission estimates, never limits.
//
//	sw.Job(plan, "integration", &Integration{}).
//	    Resources(sparkwing.Cores(2), sparkwing.MemoryGB(4))
//
// Repeated calls merge per dimension; calling with no hints clears the
// node's hints.
func (n *JobNode) Resources(hints ...ResourceHint) *JobNode {
	n.resources = applyResourceHints(n.resources, hints)
	return n
}

// ResourceHints returns a copy of the cost hints declared via
// [JobNode.Resources], or nil when the node declared none.
func (n *JobNode) ResourceHints() *ResourceHints {
	return copyResourceHints(n.resources)
}

// Resources declares the same cold-start cost hints on every member of
// the group. See [JobNode.Resources].
func (g *JobGroup) Resources(hints ...ResourceHint) *JobGroup {
	for _, m := range g.Members() {
		m.Resources(hints...)
	}
	return g
}

func applyResourceHints(current *ResourceHints, hints []ResourceHint) *ResourceHints {
	if len(hints) == 0 {
		return nil
	}
	out := &ResourceHints{}
	if current != nil {
		*out = *current
	}
	for _, h := range hints {
		if h != nil {
			h(out)
		}
	}
	return out
}

func copyResourceHints(h *ResourceHints) *ResourceHints {
	if h == nil {
		return nil
	}
	out := *h
	return &out
}
