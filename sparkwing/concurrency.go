package sparkwing

import "time"

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
	// Queue waits in FIFO order for room, then runs.
	Queue OnLimit = "queue"
	// Fail errors the node immediately.
	Fail OnLimit = "fail"
	// Skip resolves the node as a no-op without running it.
	Skip OnLimit = "skip"
	// CancelOthers evicts running members oldest-first until this node
	// fits, then runs. Eviction is best-effort: side effects a member
	// completed before the cancel signal arrived are not rolled back.
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
	// CancelTimeout bounds how long a [CancelOthers] member waits for
	// evicted holders to release before the slot is force-freed. Zero
	// uses the backend default. Only meaningful with OnLimit
	// [CancelOthers].
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
func NewConcurrencyGroup(name string, limit ConcurrencyLimit) *ConcurrencyGroup {
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

// Concurrency gates the whole run on concurrency group g: the run
// acquires one unit of g's budget before any node dispatches and
// releases it when the run reaches a terminal status. A plan never
// memoizes (it has side effects not captured in one output), so a plan
// participates in concurrency only -- there is no plan-level Cache.
func (p *Plan) Concurrency(g *ConcurrencyGroup) *Plan {
	p.concurrency = g
	return p
}

// ConcurrencyGroupRef returns the group set via [Plan.Concurrency], or
// nil when the plan declared no whole-run coordination.
func (p *Plan) ConcurrencyGroupRef() *ConcurrencyGroup {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.concurrency
}
