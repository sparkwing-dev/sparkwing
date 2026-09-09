package sparkwing

import (
	"context"
	"sync"
	"time"
)

// JobGroup holds nodes for dependency wiring and dashboard grouping.
// [GroupJobs] and [JobFanOut] fix membership during plan construction;
// [JobFanOutDynamic] populates members after its source completes.
// A named group renders under one dashboard header. An unnamed group
// serves only as a dependency target.
type JobGroup struct {
	mu      sync.Mutex
	name    string
	members []*JobNode
	dynamic bool
	ready   chan struct{}
	err     error
}

// Name returns the group's declared name, or "" for an unnamed
// (structural-only) group.
func (g *JobGroup) Name() string { return g.name }

// Members returns the group's current nodes. For dynamic groups,
// the list is populated only after Ready() closes.
func (g *JobGroup) Members() []*JobNode {
	g.mu.Lock()
	defer g.mu.Unlock()
	out := make([]*JobNode, len(g.members))
	copy(out, g.members)
	return out
}

// Dynamic reports whether this group's membership is determined at
// dispatch through [JobFanOutDynamic].
func (g *JobGroup) Dynamic() bool { return g.dynamic }

// Ready returns a channel that closes once a dynamic group's
// expansion completes, including on failure. Static groups return
// a closed channel.
func (g *JobGroup) Ready() <-chan struct{} {
	if g.ready == nil {
		ch := make(chan struct{})
		close(ch)
		return ch
	}
	return g.ready
}

// Err returns the expansion error, if any. Only meaningful after
// Ready() has closed.
func (g *JobGroup) Err() error {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.err
}

func (g *JobGroup) finalize(members []*JobNode, err error) {
	g.mu.Lock()
	g.members = append(g.members, members...)
	g.err = err
	g.mu.Unlock()
	close(g.ready)
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
// Use [JobFanOutDynamic] for items produced by an upstream job.
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

// JobFanOutDynamic registers one child per item in source's []T output
// after source completes. The callback returns an ID and a value accepted
// by [Job]. The returned group is a dependency target for every child.
// A source whose declared output differs from []T panics during planning.
func JobFanOutDynamic[T any](p *Plan, name string, source *JobNode, fn func(T) (string, any)) *JobGroup {
	if p == nil {
		panic("sparkwing: JobFanOutDynamic: plan must be non-nil")
	}
	if source == nil {
		panic("sparkwing: JobFanOutDynamic: source must be non-nil")
	}
	if fn == nil {
		panic("sparkwing: JobFanOutDynamic: fn must be non-nil")
	}
	srcRef := RefTo[[]T](source)
	g := &JobGroup{
		name:    name,
		dynamic: true,
		ready:   make(chan struct{}),
	}
	gen := func(ctx context.Context) []*JobNode {
		items := srcRef.Get(ctx)
		out := make([]*JobNode, 0, len(items))
		for _, it := range items {
			id, x := fn(it)
			job := coerceJobArg("JobFanOutDynamic", id, x)
			out = append(out, newNode("JobFanOutDynamic", id, job))
		}
		return out
	}
	p.expansions = append(p.expansions, Expansion{Source: source, Group: g, Gen: gen})
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

// Outputs declares the same artifact output globs on every member.
// See [JobNode.Outputs].
func (g *JobGroup) Outputs(globs ...string) *JobGroup {
	for _, m := range g.Members() {
		m.Outputs(globs...)
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

// Prefers boosts matching enrolled-executor offers within their priority
// ceiling for every member. See [JobNode.Prefers].
func (g *JobGroup) Prefers(labels ...string) *JobGroup {
	for _, m := range g.Members() {
		m.Prefers(labels...)
	}
	return g
}

// WhenRunner marks every member as conditional on the dispatching
// runner advertising the listed labels. See [JobNode.WhenRunner].
func (g *JobGroup) WhenRunner(labels ...string) *JobGroup {
	for _, m := range g.Members() {
		m.WhenRunner(labels...)
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

// Env sets a per-node environment variable on every member.
func (g *JobGroup) Env(key, value string) *JobGroup {
	for _, m := range g.Members() {
		m.Env(key, value)
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

// CacheDir registers dependency-directory caches on every member.
// See [JobNode.CacheDir].
func (g *JobGroup) CacheDir(caches ...DirCache) *JobGroup {
	for _, m := range g.Members() {
		m.CacheDir(caches...)
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

// NeedsOptional declares optional upstream dependencies on every
// member; unknown IDs are silently dropped at finalize. See
// [JobNode.NeedsOptional].
func (g *JobGroup) NeedsOptional(deps ...Dep) *JobGroup {
	for _, m := range g.Members() {
		m.NeedsOptional(deps...)
	}
	return g
}
