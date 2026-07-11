package sparkwing

import (
	"fmt"
	"math"
)

// ResourceHints is the resolved resource pin declared via
// [Plan.Resources] or [JobNode.Resources]. A pin is an explicit,
// authoritative statement of peak usage: when present, admission charges
// it verbatim and polices it, warning when it drifts from what the
// pipeline actually costs. Pins are the exception, not the norm -- the
// posture is "declare nothing, sparkwing measures; pin sparingly and
// sparkwing polices the pin." An absent pin means admission measures the
// pipeline and admits from those measurements. A pin is still not a limit
// -- work that exceeds it is not throttled or killed; it sets the charge
// admission reserves, nothing more.
type ResourceHints struct {
	// Cores is the pinned peak number of CPU cores the work uses.
	// Fractional values are meaningful (0.5 = half a core). Zero means
	// cores were not pinned.
	Cores float64
	// MemoryBytes is the pinned peak resident memory in bytes. Authors
	// express this via [MemoryGB]; it is stored in bytes. Zero means
	// memory was not pinned.
	MemoryBytes int64
}

// ResourceHint is one option for [Plan.Resources] or
// [JobNode.Resources]. Construct via [Cores] or [MemoryGB].
type ResourceHint func(*ResourceHints)

// Cores pins the work's peak to n CPU cores. Fractional values are
// meaningful: Cores(0.5) pins light, mostly-waiting work. Panics unless
// n is a positive, finite number.
func Cores(n float64) ResourceHint {
	if n <= 0 || math.IsInf(n, 0) || math.IsNaN(n) {
		panic(fmt.Sprintf("sparkwing: Cores(%v): hint must be a positive, finite number of cores", n))
	}
	return func(h *ResourceHints) { h.Cores = n }
}

// MemoryGB pins the work's peak to n gigabytes of resident memory, where
// one gigabyte is 2^30 bytes. The value is stored in bytes (see
// [ResourceHints.MemoryBytes]). Panics unless n is a positive, finite
// number.
func MemoryGB(n float64) ResourceHint {
	if n <= 0 || math.IsInf(n, 0) || math.IsNaN(n) {
		panic(fmt.Sprintf("sparkwing: MemoryGB(%v): hint must be a positive, finite number of gigabytes", n))
	}
	return func(h *ResourceHints) { h.MemoryBytes = int64(math.Round(n * float64(1<<30))) }
}

// Resources pins the whole run's peak CPU and memory: an explicit,
// authoritative charge admission uses verbatim instead of measuring the
// pipeline. Pin sparingly. Most pipelines declare nothing and let
// sparkwing measure real runs; reach for a pin only where measurement
// misleads -- a spiky workload whose peak the sampler rarely catches,
// work that must never share the host, or pre-sizing a brand-new pipeline
// before any profile exists. A pin is authoritative, so sparkwing polices
// it: when a pin drifts far from what the pipeline actually costs, the run
// ends with a one-line warning to update or remove it. A pin is not a
// limit -- it never caps or kills the work.
//
//	func (p *Deploy) Plan(ctx context.Context, plan *sparkwing.Plan, in Inputs, rc sparkwing.RunContext) error {
//	    plan.Resources(sparkwing.Cores(4), sparkwing.MemoryGB(8))
//	    ...
//	}
//
// Repeated calls merge: each pin overwrites the same dimension set by an
// earlier call and leaves the other dimensions intact. Calling with no
// arguments clears every plan-level pin.
func (p *Plan) Resources(hints ...ResourceHint) *Plan {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.resources = applyResourceHints(p.resources, hints)
	return p
}

// ResourceHints returns a copy of the plan-level resource pin declared
// via [Plan.Resources], or nil when the plan declared none.
func (p *Plan) ResourceHints() *ResourceHints {
	p.mu.Lock()
	defer p.mu.Unlock()
	return copyResourceHints(p.resources)
}

// Resources pins this node's peak CPU and memory. Same semantics as
// [Plan.Resources], scoped to one node: an explicit, authoritative charge
// admission uses instead of measuring, policed for drift, never a limit.
// Pin sparingly -- prefer letting sparkwing measure.
//
//	sw.Job(plan, "integration", &Integration{}).
//	    Resources(sparkwing.Cores(2), sparkwing.MemoryGB(4))
//
// Repeated calls merge per dimension; calling with no arguments clears the
// node's pin.
func (n *JobNode) Resources(hints ...ResourceHint) *JobNode {
	n.resources = applyResourceHints(n.resources, hints)
	return n
}

// ResourceHints returns a copy of the resource pin declared via
// [JobNode.Resources], or nil when the node declared none.
func (n *JobNode) ResourceHints() *ResourceHints {
	return copyResourceHints(n.resources)
}

// Resources pins the same peak on every member of the group. See
// [JobNode.Resources].
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
