package sparkwing

import (
	"fmt"
	"strconv"
	"time"
)

// Scope selects how far a [ConcurrencyGroup]'s budget reaches: only the
// nodes of one run, every run on one machine, or the whole fleet
// coordinating through a shared backend.
type Scope string

const (
	// ScopeRun bounds the budget to the nodes of a single run.
	ScopeRun Scope = "run"
	// ScopeBox bounds the budget to one machine, even when a controller
	// dispatches several runs to it.
	ScopeBox Scope = "box"
	// ScopeGlobal pools the budget across every run that names the
	// group, coordinated through the shared backend. The zero value.
	ScopeGlobal Scope = "global"
)

// OnLimit is the closed set of behaviors for a node that finds its
// [ConcurrencyGroup] at capacity. Sharing another member's result is
// not among them: a group is different work taking turns, never the
// same work -- result reuse is [JobNode.Cache]'s job.
type OnLimit string

const (
	// Queue waits for room, then runs. Waiters that fit run oldest-first; a
	// waiter that cannot fit in the currently available weighted budget does
	// not block later waiters that do fit unless younger backfilled holders
	// are what keep the older waiter from fitting.
	Queue OnLimit = "queue"
	// Fail errors the node immediately.
	Fail OnLimit = "fail"
	// Skip resolves the node as a no-op without running it.
	Skip OnLimit = "skip"
	// CancelOthers is best-effort preemption ("newest wins"): it evicts
	// running members oldest-first until this node fits, then takes the
	// slot and runs immediately. Eviction is cooperative -- an evicted
	// member is signaled to stop and winds down on its own -- so this node
	// may briefly run alongside a still-draining victim, and side effects a
	// member completed before the cancel signal are not rolled back. Use
	// [Queue] when you need strict mutual exclusion with no overlap.
	CancelOthers OnLimit = "cancel_others"
)

// ConcurrencyLimit is the budget a [ConcurrencyGroup] enforces.
// Capacity and member cost are plain integers in author-defined units
// -- a slot, a gigabyte, a database container -- so count-limiting is
// the degenerate case (capacity N, every member cost 1).
type ConcurrencyLimit struct {
	// Capacity is the total budget in author-defined units. With the
	// default member cost of 1 it reads as "max members running at
	// once". Values <= 0 are treated as 1 by the coordination backend.
	//
	// When two live participants declare different capacities for the
	// same group (a version skew across runs), the effective capacity
	// is the minimum -- a cap is a safety constraint, so lowering takes
	// effect immediately and raising waits for the lower declaration to
	// drain.
	Capacity int
	// Scope is how far the budget reaches (see [Scope]). The zero value
	// is [ScopeGlobal].
	Scope Scope
	// OnLimit is what a member does when the group is full. The zero
	// value is [Queue].
	OnLimit OnLimit
	// QueueTimeout bounds how long a [Queue] member waits for room
	// before failing with failure_reason "queue_timeout". Zero waits
	// indefinitely. Only meaningful with OnLimit [Queue].
	QueueTimeout time.Duration
	// CancelTimeout bounds how long evicted holders have to cooperatively
	// release before they are force-released, so a stuck victim can't pin
	// the budget indefinitely. Zero uses the backend default. Only
	// meaningful with OnLimit [CancelOthers].
	CancelTimeout time.Duration
}

// ConcurrencyGroup is a named budget that member nodes share. Define it
// once -- package-level for a literal budget, or inside Plan() when the
// capacity comes from a per-machine arg -- and pass the handle to each
// member via [JobNode.Concurrency]. Because capacity lives in exactly
// one place, members cannot disagree on it within a pipeline.
//
//	var dbGroup = sparkwing.NewConcurrencyGroup("db", sparkwing.ConcurrencyLimit{
//	    Capacity: 2,
//	    OnLimit:  sparkwing.Queue,
//	})
//
//	sparkwing.Job(plan, "shard-1", run).Concurrency(dbGroup)
//	sparkwing.Job(plan, "shard-2", run).Concurrency(dbGroup)
type ConcurrencyGroup struct {
	name  string
	limit ConcurrencyLimit
}

// NewConcurrencyGroup constructs a [ConcurrencyGroup] named name with
// the given limit. The name is the coordination key the backend uses,
// so two pipelines that pass the same name share one budget.
//
// An empty name or an unknown Scope / OnLimit value panics at
// construction: a misspelled policy would otherwise fall through to
// the backend's default and silently coordinate differently than the
// author wrote.
func NewConcurrencyGroup(name string, limit ConcurrencyLimit) *ConcurrencyGroup {
	if name == "" {
		panic("sparkwing: NewConcurrencyGroup: name must not be empty (all unnamed groups would share one budget)")
	}
	switch limit.Scope {
	case "", ScopeRun, ScopeBox, ScopeGlobal:
	default:
		panic(fmt.Sprintf(
			"sparkwing: NewConcurrencyGroup(%q): Scope %q is not one of sparkwing.ScopeRun / ScopeBox / ScopeGlobal",
			name, limit.Scope,
		))
	}
	switch limit.OnLimit {
	case "", Queue, Fail, Skip, CancelOthers:
	default:
		panic(fmt.Sprintf(
			"sparkwing: NewConcurrencyGroup(%q): OnLimit %q is not one of sparkwing.Queue / Fail / Skip / CancelOthers",
			name, limit.OnLimit,
		))
	}
	return &ConcurrencyGroup{name: name, limit: limit}
}

// Name returns the group's coordination key.
func (g *ConcurrencyGroup) Name() string { return g.name }

// Limit returns the group's declared budget.
func (g *ConcurrencyGroup) Limit() ConcurrencyLimit { return g.limit }

// Concurrency enrolls the node in concurrency group g with the given
// admission cost (default 1). The node competes with the group's other
// members for g's budget; how it behaves at the limit follows g's
// [OnLimit]. Independent of [JobNode.Cache] -- a node may declare both,
// neither, or either.
//
//	shard := sparkwing.Job(plan, "shard-1", run)
//	shard.Concurrency(dbGroup, 4) // this member draws 4 units of the budget
//
// Repeated calls overwrite. Passing more than one cost panics.
func (n *JobNode) Concurrency(g *ConcurrencyGroup, cost ...int) *JobNode {
	if g == nil {
		n.concurrency = nil
		return n
	}
	c := concurrencyCost(g, "node "+strconv.Quote(n.id), cost...)
	n.concurrency = &concurrencyMembership{group: g, cost: c}
	return n
}

// concurrencyMembership records a node's enrollment in a
// [ConcurrencyGroup] plus the admission cost it declared.
type concurrencyMembership struct {
	group *ConcurrencyGroup
	cost  int
}

// ConcurrencyGroupRef returns the group the node joined via
// [JobNode.Concurrency], or nil when the node declared no membership.
func (n *JobNode) ConcurrencyGroupRef() *ConcurrencyGroup {
	if n.concurrency == nil {
		return nil
	}
	return n.concurrency.group
}

// ConcurrencyCost returns the admission cost declared via
// [JobNode.Concurrency], or 0 when the node has no membership. The
// coordination backend admits a member only when the summed cost of
// live members in the same scope plus this cost fits the capacity.
func (n *JobNode) ConcurrencyCost() int {
	if n.concurrency == nil {
		return 0
	}
	return n.concurrency.cost
}

// Concurrency enrolls every member of the group in concurrency group g
// with the given admission cost. See [JobNode.Concurrency].
func (g *JobGroup) Concurrency(cg *ConcurrencyGroup, cost ...int) *JobGroup {
	for _, m := range g.Members() {
		m.Concurrency(cg, cost...)
	}
	return g
}

// PlanConcurrency records one whole-run concurrency gate.
type PlanConcurrency struct {
	Group *ConcurrencyGroup
	Cost  int
}

// Concurrency gates the whole run on concurrency group g: the run acquires
// each declared plan-level budget before any node dispatches and releases
// it when the run reaches a terminal status. Cost defaults to one. Repeated
// calls compose independent gates, so authors can combine a deploy mutex with
// CPU and memory budgets. Passing nil clears every plan-level gate.
//
// A plan never memoizes (it has side effects not captured in one output), so a
// plan participates in concurrency only -- there is no plan-level Cache.
func (p *Plan) Concurrency(g *ConcurrencyGroup, cost ...int) *Plan {
	p.mu.Lock()
	defer p.mu.Unlock()
	if g == nil {
		p.planConcurrency = nil
		return p
	}
	c := concurrencyCost(g, "plan", cost...)
	membership := PlanConcurrency{Group: g, Cost: c}
	for i, existing := range p.planConcurrency {
		if sameConcurrencyGroup(existing.Group, g) {
			p.planConcurrency[i] = membership
			return p
		}
	}
	p.planConcurrency = append(p.planConcurrency, membership)
	return p
}

// PlanConcurrency returns every whole-run gate declared via [Plan.Concurrency].
func (p *Plan) PlanConcurrency() []PlanConcurrency {
	p.mu.Lock()
	defer p.mu.Unlock()
	out := make([]PlanConcurrency, len(p.planConcurrency))
	copy(out, p.planConcurrency)
	return out
}

// ConcurrencyGroupRef returns the first group set via [Plan.Concurrency], or
// nil when the plan declared no whole-run coordination. Prefer
// [Plan.PlanConcurrency] when a caller must observe every whole-run gate.
func (p *Plan) ConcurrencyGroupRef() *ConcurrencyGroup {
	p.mu.Lock()
	defer p.mu.Unlock()
	if len(p.planConcurrency) == 0 {
		return nil
	}
	return p.planConcurrency[0].Group
}

// ConcurrencyCost returns the first plan-level admission cost declared via
// [Plan.Concurrency], or 0 when the plan declared no whole-run coordination.
// Prefer [Plan.PlanConcurrency] when a caller must observe every gate.
func (p *Plan) ConcurrencyCost() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	if len(p.planConcurrency) == 0 {
		return 0
	}
	return p.planConcurrency[0].Cost
}

func sameConcurrencyGroup(a, b *ConcurrencyGroup) bool {
	if a == nil || b == nil {
		return a == b
	}
	return a.name == b.name && a.limit.Scope == b.limit.Scope
}

func concurrencyCost(g *ConcurrencyGroup, subject string, cost ...int) int {
	c := 1
	switch len(cost) {
	case 0:
	case 1:
		c = cost[0]
	default:
		panic("sparkwing: Concurrency: at most one cost may be given")
	}
	if c < 1 {
		c = 1
	}
	if eff := g.limit.Capacity; eff > 0 && c > eff {
		panic(fmt.Sprintf(
			"sparkwing: Concurrency: %s cost %d exceeds group %q capacity %d -- it could never be admitted",
			subject, c, g.name, eff,
		))
	} else if eff <= 0 && c > 1 {
		panic(fmt.Sprintf(
			"sparkwing: Concurrency: %s cost %d exceeds group %q capacity 1 (capacity unset defaults to 1) -- it could never be admitted",
			subject, c, g.name,
		))
	}
	return c
}
