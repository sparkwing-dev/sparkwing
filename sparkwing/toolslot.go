package sparkwing

import (
	"context"
	"fmt"
	"time"
)

// ToolSlotProvider acquires cost units of a [ConcurrencyGroup] from
// inside a running job body and returns the release. The orchestrator
// installs one on every node's context; [ToolSlot] is the SDK-facing
// side that pipeline authors call.
//
// It exists as a context-carried function rather than a direct call
// because the acquiring code lives in the orchestrator, which already
// imports this package, so a direct call would close an import cycle.
type ToolSlotProvider func(ctx context.Context, group *ConcurrencyGroup, cost int) (release func(), err error)

type toolSlotCtxKey struct{}

// WithToolSlotProvider returns ctx carrying p, so [ToolSlot] called from
// a job body reaches the daemon this run was admitted by. The
// orchestrator calls this; pipeline authors do not.
func WithToolSlotProvider(ctx context.Context, p ToolSlotProvider) context.Context {
	return context.WithValue(ctx, toolSlotCtxKey{}, p)
}

func toolSlotProviderFrom(ctx context.Context) ToolSlotProvider {
	p, _ := ctx.Value(toolSlotCtxKey{}).(ToolSlotProvider)
	return p
}

// ToolSlot takes cost units of g for the duration of one step inside an
// already-running job, and reports whether it got them. Use it to bound
// a box-wide external tool -- a linter, a compiler, a container build --
// that holds a private lock the daemon cannot see:
//
//	release, granted := sparkwing.ToolSlot(ctx, lintBudget, lintCost)
//	defer release()
//	flag := "--allow-serial-runners"
//	if granted {
//	    flag = "--allow-parallel-runners"
//	}
//
// While it waits, the step reports its queue position the same way a
// queued node does, so a slow gate can say what it is slow behind.
//
// granted is false when the run has no admission daemon behind it, when
// the wait exceeded the group's QueueTimeout, or when the daemon became
// unreachable mid-run. It never blocks forever unless the group declares
// no QueueTimeout, and it never returns an error, because the caller's
// decision is binary: hold the budget, or fall back to whatever private
// serialization the tool ships with. A false return is narrated to the
// run log naming the reason, because a step that quietly stopped being
// bounded is the one failure mode worth never inventing.
//
// The returned release is always non-nil and is safe to call when
// granted is false, so `defer release()` needs no guard.
//
// Callers MUST NOT drop the tool's own lock on a false return. The slot
// is the only thing serializing the tool once its private lock is gone,
// so "not granted" has to mean "fall back", never "proceed unbounded".
func ToolSlot(ctx context.Context, g *ConcurrencyGroup, cost ...int) (release func(), granted bool) {
	noop := func() {}
	if g == nil {
		return noop, false
	}
	c := concurrencyCost(g, "tool slot "+g.Name(), cost...)

	provider := toolSlotProviderFrom(ctx)
	if provider == nil {
		Warn(ctx, "tool slot %q: no admission daemon behind this run, falling back to the tool's own serialization", g.Name())
		return noop, false
	}
	rel, err := provider(ctx, g, c)
	if err != nil {
		Warn(ctx, "tool slot %q: not granted (%v), falling back to the tool's own serialization", g.Name(), err)
		return noop, false
	}
	if rel == nil {
		rel = noop
	}
	return rel, true
}

// BoxToolBudget builds the conventional [ConcurrencyGroup] for one
// external tool sharing one machine: a box-scoped budget measured in
// hundredths of a core, which queues rather than failing.
//
// Capacity is in centicores because a budget expressed as a slot count
// cannot survive a change in what the tool costs. A tool whose scope
// widens gets more expensive without the author noticing, and a count
// keeps admitting the old number of them; a cost in cores re-derives the
// concurrency automatically from one measured number.
//
// grantableCores is what the machine will lend to this class of work,
// which is its core count minus whatever the operator reserves.
func BoxToolBudget(tool string, grantableCores float64, queueTimeout time.Duration) *ConcurrencyGroup {
	capacity := int(grantableCores * 100)
	if capacity < 1 {
		capacity = 1
	}
	return NewConcurrencyGroup(tool, ConcurrencyLimit{
		Capacity:     capacity,
		Scope:        ScopeBox,
		OnLimit:      Queue,
		QueueTimeout: queueTimeout,
	})
}

// ToolCostCenticores converts a measured per-invocation core cost into
// the integer units [BoxToolBudget] counts in, clamping to at least one
// so a tool measured at near-zero still draws a unit and cannot be
// admitted without limit.
func ToolCostCenticores(cores float64) int {
	c := int(cores * 100)
	if c < 1 {
		c = 1
	}
	return c
}

// String renders the group for a log line naming what a step is bounded
// by, so "the gate is slow" has an answer that names the budget.
func (g *ConcurrencyGroup) String() string {
	return fmt.Sprintf("%s(capacity %d, scope %s, on-limit %s)",
		g.name, g.limit.Capacity, g.limit.scopeOrDefault(), g.limit.onLimitOrDefault())
}

func (l ConcurrencyLimit) scopeOrDefault() Scope {
	if l.Scope == "" {
		return ScopeGlobal
	}
	return l.Scope
}

func (l ConcurrencyLimit) onLimitOrDefault() OnLimit {
	if l.OnLimit == "" {
		return Queue
	}
	return l.OnLimit
}
