package sparkwing

import (
	"context"
	"sync"
	"time"
)

// NodeGroup is a handle to a set of nodes. Static groups (from
// sparkwing.GroupJobs or sparkwing.JobFanOut) fix their members at
// plan-construction; dynamic groups (from sparkwing.JobFanOutDynamic)
// populate them at dispatch-time after the generator runs.
//
// Named groups render as a single collapsible cluster in the
// dashboard DAG view. The empty name means "structural collection
// only" -- still a Needs target, but no UI cluster.
//
// Downstream `.Needs(group)` expands eagerly for static groups and
// waits on `<-group.Ready()` for dynamic ones.
type NodeGroup struct {
	mu      sync.Mutex
	name    string
	members []*Node
	dynamic bool
	ready   chan struct{}
	err     error
}

// Name returns the group's declared name, or "" for an unnamed
// (structural-only) group.
func (g *NodeGroup) Name() string { return g.name }

// Members returns the group's current nodes. For dynamic groups,
// the list is populated only after Ready() closes.
func (g *NodeGroup) Members() []*Node {
	g.mu.Lock()
	defer g.mu.Unlock()
	out := make([]*Node, len(g.members))
	copy(out, g.members)
	return out
}

// Dynamic reports whether this group's membership is determined at
// dispatch-time (ExpandFrom) rather than plan-construction (GroupJobs).
func (g *NodeGroup) Dynamic() bool { return g.dynamic }

// Ready returns a channel that closes once a dynamic group's
// expansion completes (success or failure). Static groups return a
// pre-closed channel so callers can treat both uniformly.
func (g *NodeGroup) Ready() <-chan struct{} {
	if g.ready == nil {
		ch := make(chan struct{})
		close(ch)
		return ch
	}
	return g.ready
}

// Err returns the expansion error, if any. Only meaningful after
// Ready() has closed.
func (g *NodeGroup) Err() error {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.err
}

// Finalize populates a dynamic group with the generator's output and
// signals readiness. Called by the orchestrator.
func (g *NodeGroup) Finalize(members []*Node, err error) {
	g.mu.Lock()
	g.members = append(g.members, members...)
	g.err = err
	g.mu.Unlock()
	close(g.ready)
}

// GroupJobs declares a named bundle of existing Plan nodes. The
// returned *NodeGroup is both a Needs target (downstream depends on
// every member) and a dashboard cluster (rendered as a single visual
// unit under the given name). An empty name means "structural
// collection only" -- still a Needs target, but no UI cluster.
//
// The Work-layer mirror is sparkwing.GroupSteps; both follow the
// noun-prefix convention so tab-complete on Group* surfaces every
// grouping verb.
//
//	build := sw.Job(plan, "build", &BuildJob{})
//	checks := sw.GroupJobs(plan, "safety",
//	    sw.Job(plan, "lint",     &LintJob{}).Needs(build),
//	    sw.Job(plan, "security", &SecurityJob{}).Needs(build),
//	    sw.Job(plan, "test",     &TestJob{}).Needs(build),
//	)
//	sw.Job(plan, "deploy", &DeployJob{}).Needs(checks)
//
// In the dashboard, lint+security+test collapse into a "safety"
// cluster and a single arrow renders from the cluster to deploy.
//
// For an unnamed structural collection (no UI cluster), pass name="".
func GroupJobs(p *Plan, name string, nodes ...*Node) *NodeGroup {
	if p == nil {
		panic("sparkwing: GroupJobs: plan must be non-nil")
	}
	g := &NodeGroup{name: name, members: nodes}
	p.groups = append(p.groups, g)
	return g
}

// JobFanOut is the Plan-time static fan-out helper. items is in hand
// at Plan() time; one Node is registered per element via sw.Job.
// Returns a *NodeGroup named `name`, suitable for `.Needs(group)` from
// downstream consumers and for dashboard cluster rendering.
//
// items may be empty -- the returned *NodeGroup has no members and
// .Needs(group) becomes a no-op edge.
//
// The per-item fn's second return value accepts the same shapes as
// sparkwing.Job's third arg (a Workable struct or a bare func(ctx)
// error closure); the SDK coerces uniformly.
//
//	images := sw.JobFanOut(plan, "image-builds", Images, func(img imageSpec) (string, any) {
//	    return "build-" + img.Name, &BuildImageJob{Image: img}
//	}).Needs(webBuild, discover)
//	sw.Job(plan, "artifact", &ArtifactJob{}).Needs(images)
//
// Implemented as a free function because Go does not allow type
// parameters on methods. For runtime fan-out (slice produced by an
// upstream Node's typed output), use JobFanOutDynamic instead.
func JobFanOut[T any](p *Plan, name string, items []T, fn func(T) (string, any)) *NodeGroup {
	if p == nil {
		panic("sparkwing: JobFanOut: plan must be non-nil")
	}
	if fn == nil {
		panic("sparkwing: JobFanOut: fn must be non-nil")
	}
	members := make([]*Node, 0, len(items))
	for _, it := range items {
		id, job := fn(it)
		members = append(members, Job(p, id, job))
	}
	g := &NodeGroup{name: name, members: members}
	p.groups = append(p.groups, g)
	return g
}

// JobFanOutDynamic is the runtime fan-out helper. source is a Node
// whose typed output is []T; after source completes, fn runs once per
// element and contributes a fresh child Node per item. Returns a
// *NodeGroup named `name`, suitable for `.Needs(group)` from downstream
// consumers and for dashboard cluster rendering.
//
//	// DiscoverServices embeds sparkwing.Produces[[]string]
//	discover := sw.Job(plan, "discover", &DiscoverServices{})
//	builds   := sw.JobFanOutDynamic(plan, "service-builds", discover, func(svc string) (string, any) {
//	    s := svc
//	    return "build-" + s, func(ctx context.Context) error { return build(ctx, s) }
//	})
//	sw.Job(plan, "publish", &PublishJob{}).Needs(builds)
//
// The source must produce []T (the cardinality-many case): its job
// must embed sparkwing.Produces[[]T] AND its Work must return a
// sparkwing.Step whose fn signature is func(ctx) ([]T, error).
// RefTo[[]T](source) Plan-time-validates the contract and panics with
// a node-id-tagged message on mismatch. For Plan-time fan-out (slice
// known at Plan() time), use JobFanOut.
//
// The per-item fn's second return value accepts the same shapes as
// sparkwing.Job's third arg (Workable struct or func(ctx) error).
func JobFanOutDynamic[T any](p *Plan, name string, source *Node, fn func(T) (string, any)) *NodeGroup {
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
	g := &NodeGroup{
		name:    name,
		dynamic: true,
		ready:   make(chan struct{}),
	}
	gen := func(ctx context.Context) []*Node {
		items := srcRef.Get(ctx)
		out := make([]*Node, 0, len(items))
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

// Group chainable modifiers delegate to every current member,
// returning the same *NodeGroup so calls can chain. For dynamic groups
// (JobFanOutDynamic), only members materialized at call time are
// affected; modifiers applied to runtime-fan-out groups should
// typically be set on the generator's per-element Job instead.
//
// OnFailure is intentionally NOT mirrored on *NodeGroup: recovery
// handlers are per-node by intent.

// Needs declares an upstream dependency on every member of the group.
// Accepts the same shapes as Node.Needs (*Node, *NodeGroup, []*Node, string).
func (g *NodeGroup) Needs(deps ...any) *NodeGroup {
	for _, m := range g.Members() {
		m.Needs(deps...)
	}
	return g
}

// Retry configures every member to be re-attempted up to attempts
// additional times on failure. See Node.Retry.
func (g *NodeGroup) Retry(attempts int, opts ...RetryOption) *NodeGroup {
	for _, m := range g.Members() {
		m.Retry(attempts, opts...)
	}
	return g
}

// Timeout caps the per-attempt duration on every member. See Node.Timeout.
func (g *NodeGroup) Timeout(d time.Duration) *NodeGroup {
	for _, m := range g.Members() {
		m.Timeout(d)
	}
	return g
}

// RunsOn restricts every member to runners advertising the given labels.
// See Node.RunsOn.
func (g *NodeGroup) RunsOn(labels ...string) *NodeGroup {
	for _, m := range g.Members() {
		m.RunsOn(labels...)
	}
	return g
}

// SkipIf registers a predicate on every member. See Node.SkipIf.
func (g *NodeGroup) SkipIf(fn SkipPredicate, opts ...SkipOption) *NodeGroup {
	for _, m := range g.Members() {
		m.SkipIf(fn, opts...)
	}
	return g
}

// Env sets a per-node environment variable on every member.
func (g *NodeGroup) Env(key, value string) *NodeGroup {
	for _, m := range g.Members() {
		m.Env(key, value)
	}
	return g
}

// Inline marks every member for in-process execution. See Node.Inline.
func (g *NodeGroup) Inline() *NodeGroup {
	for _, m := range g.Members() {
		m.Inline()
	}
	return g
}

// ContinueOnError marks every member so downstream dependents proceed
// even on failure. See Node.ContinueOnError.
func (g *NodeGroup) ContinueOnError() *NodeGroup {
	for _, m := range g.Members() {
		m.ContinueOnError()
	}
	return g
}

// Optional marks every member as non-essential. See Node.Optional.
func (g *NodeGroup) Optional() *NodeGroup {
	for _, m := range g.Members() {
		m.Optional()
	}
	return g
}

// BeforeRun registers a pre-run hook on every member. See Node.BeforeRun.
func (g *NodeGroup) BeforeRun(fn BeforeRunFn) *NodeGroup {
	for _, m := range g.Members() {
		m.BeforeRun(fn)
	}
	return g
}

// AfterRun registers a post-run hook on every member. See Node.AfterRun.
func (g *NodeGroup) AfterRun(fn AfterRunFn) *NodeGroup {
	for _, m := range g.Members() {
		m.AfterRun(fn)
	}
	return g
}

// Cache applies the given cache options to every member. See Node.Cache.
func (g *NodeGroup) Cache(opts CacheOptions) *NodeGroup {
	for _, m := range g.Members() {
		m.Cache(opts)
	}
	return g
}

// NeedsOptional declares optional upstream dependencies on every
// member; unknown IDs are silently dropped at finalize. See
// Node.NeedsOptional.
func (g *NodeGroup) NeedsOptional(deps ...any) *NodeGroup {
	for _, m := range g.Members() {
		m.NeedsOptional(deps...)
	}
	return g
}
