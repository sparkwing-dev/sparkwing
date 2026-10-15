package sparkwing

import (
	"time"
)

// JobGroup holds nodes for dependency wiring and dashboard grouping.
// A named group renders under one dashboard header. An unnamed group
// serves only as a dependency target.
type JobGroup struct {
	name    string
	members []*JobNode
}

// Name returns the group's declared name, or "" for an unnamed
// (structural-only) group.
func (g *JobGroup) Name() string { return g.name }

// Members returns the group's jobs in declaration order.
func (g *JobGroup) Members() []*JobNode {
	out := make([]*JobNode, len(g.members))
	copy(out, g.members)
	return out
}

// GroupJobs groups existing Plan nodes under name and returns a dependency
// target covering every member. A non-empty name adds a dashboard group.
func GroupJobs(p *Plan, name string, nodes ...*JobNode) *JobGroup {
	if p == nil {
		panic("sparkwing: GroupJobs: plan must be non-nil")
	}
	g := &JobGroup{name: name, members: nodes}
	p.groups = append(p.groups, g)
	return g
}

// JobFanOut registers one job per item during plan construction.
// The callback returns an ID and a job value accepted by [Job].
// An empty items slice creates a group with no dependencies to satisfy.
func JobFanOut[T any](p *Plan, name string, items []T, fn func(T) (string, any)) *JobGroup {
	if p == nil {
		panic("sparkwing: JobFanOut: plan must be non-nil")
	}
	if fn == nil {
		panic("sparkwing: JobFanOut: fn must be non-nil")
	}
	members := make([]*JobNode, 0, len(items))
	for _, it := range items {
		id, job := fn(it)
		members = append(members, Job(p, id, job))
	}
	g := &JobGroup{name: name, members: members}
	p.groups = append(p.groups, g)
	return g
}

// Needs declares an upstream dependency on every member of the group.
// Accepts any [Dep], same as [JobNode.Needs].
func (g *JobGroup) Needs(deps ...Dep) *JobGroup {
	for _, m := range g.Members() {
		m.Needs(deps...)
	}
	return g
}

// Retry configures every member to be re-attempted up to attempts
// additional times on failure. See [JobNode.Retry].
func (g *JobGroup) Retry(attempts int, opts ...RetryOption) *JobGroup {
	for _, m := range g.Members() {
		m.Retry(attempts, opts...)
	}
	return g
}

// Timeout caps the per-attempt duration on every member. See [JobNode.Timeout].
func (g *JobGroup) Timeout(d time.Duration) *JobGroup {
	for _, m := range g.Members() {
		m.Timeout(d)
	}
	return g
}

// NoProgressTimeout sets the per-attempt inactivity timeout on every member.
// See [JobNode.NoProgressTimeout].
func (g *JobGroup) NoProgressTimeout(d time.Duration) *JobGroup {
	for _, m := range g.Members() {
		m.NoProgressTimeout(d)
	}
	return g
}

// Verify registers a postcondition check on every member. See [JobNode.Verify].
func (g *JobGroup) Verify(fn VerifyFn) *JobGroup {
	for _, m := range g.Members() {
		m.Verify(fn)
	}
	return g
}

// Consumes stages the given producer's artifacts into every member's
// workspace before it runs, and implies Needs(producer) on each. See
// [JobNode.Consumes].
func (g *JobGroup) Consumes(producer *JobNode, opts ...ConsumeOption) *JobGroup {
	for _, m := range g.Members() {
		m.Consumes(producer, opts...)
	}
	return g
}

// Requires constrains runner claims for every non-inline dispatched member.
// An unmatched member waits until the controller fails it with queue_timeout.
// Direct runs and inline jobs have no runner claim step. See [JobNode.Requires].
func (g *JobGroup) Requires(labels ...string) *JobGroup {
	for _, m := range g.Members() {
		m.Requires(labels...)
	}
	return g
}

// NeedsTools restricts every member to agents that have the named tools. See
// [JobNode.NeedsTools].
func (g *JobGroup) NeedsTools(names ...string) *JobGroup {
	mustKnowTools("JobGroup.NeedsTools", names)
	for _, m := range g.Members() {
		m.NeedsTools(names...)
	}
	return g
}

// Prefers sets the same runner-label preference on every member: it holds the
// node back from a runner that does not advertise it. See [JobNode.Prefers].
func (g *JobGroup) Prefers(labels ...string) *JobGroup {
	for _, m := range g.Members() {
		m.Prefers(labels...)
	}
	return g
}

// SkipIf registers a predicate on every member. See [JobNode.SkipIf].
func (g *JobGroup) SkipIf(fn SkipPredicate, opts ...SkipOption) *JobGroup {
	for _, m := range g.Members() {
		m.SkipIf(fn, opts...)
	}
	return g
}

// Inline places every member on the dispatcher's host. See [JobNode.Inline].
func (g *JobGroup) Inline() *JobGroup {
	for _, m := range g.Members() {
		m.Inline()
	}
	return g
}

// ContinueOnError marks every member so downstream dependents proceed
// even on failure. See [JobNode.ContinueOnError].
func (g *JobGroup) ContinueOnError() *JobGroup {
	for _, m := range g.Members() {
		m.ContinueOnError()
	}
	return g
}

// Optional marks every member as non-essential. See [JobNode.Optional].
func (g *JobGroup) Optional() *JobGroup {
	for _, m := range g.Members() {
		m.Optional()
	}
	return g
}

// BeforeRun registers a pre-run hook on every member. See [JobNode.BeforeRun].
func (g *JobGroup) BeforeRun(fn BeforeRunFn) *JobGroup {
	for _, m := range g.Members() {
		m.BeforeRun(fn)
	}
	return g
}

// AfterRun registers a post-run hook on every member. See [JobNode.AfterRun].
func (g *JobGroup) AfterRun(fn AfterRunFn) *JobGroup {
	for _, m := range g.Members() {
		m.AfterRun(fn)
	}
	return g
}
