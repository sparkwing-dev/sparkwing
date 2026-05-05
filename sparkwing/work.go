package sparkwing

import (
	"context"
	"fmt"
	"reflect"
	"slices"
	"sync"
)

// Workable is the interface every dispatchable Job satisfies: a struct
// that exposes its inner DAG via Work(). Author types embed
// sparkwing.Base (and optionally sparkwing.Produces[T]); the SDK
// materializes the inner Work at Plan-time so renderers see the full
// graph before any dispatch begins.
//
// For the trivial single-step case use sparkwing.JobFn; for typed
// jobs declare a struct embedding sparkwing.Produces[T] and emit the
// result via sparkwing.Result / Out + Work.SetResult.
type Workable interface {
	Work() *Work
}

// Work is the inner DAG of a Job. Mirrors Plan at the inner layer:
// Steps with Needs / SkipIf, plus Sequence / Parallel combinators and
// Spawn primitives for layer escape.
//
// Build via NewWork. The orchestrator calls Job.Work() once per Node
// at Plan-time and walks the entire reachable graph (including Spawn
// targets) before any dispatch.
type Work struct {
	mu         sync.Mutex
	steps      []*WorkStep
	byID       map[string]*WorkStep
	spawns     []*SpawnSpec
	spawnGens  []*SpawnGenSpec
	resultStep *WorkStep
	resultType reflect.Type
}

// NewWork returns an empty Work.
func NewWork() *Work {
	return &Work{byID: map[string]*WorkStep{}}
}

// Steps returns the work's steps in insertion order.
func (w *Work) Steps() []*WorkStep {
	w.mu.Lock()
	defer w.mu.Unlock()
	out := make([]*WorkStep, len(w.steps))
	copy(out, w.steps)
	return out
}

// StepByID returns the step with the given id, or nil if absent.
func (w *Work) StepByID(id string) *WorkStep {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.byID[id]
}

// Spawns returns the static SpawnNode declarations registered on
// this Work.
func (w *Work) Spawns() []*SpawnSpec {
	out := make([]*SpawnSpec, len(w.spawns))
	copy(out, w.spawns)
	return out
}

// SpawnGens returns the SpawnNodeForEach declarations. Shape is
// known at Plan-time; cardinality is decided at dispatch.
func (w *Work) SpawnGens() []*SpawnGenSpec {
	out := make([]*SpawnGenSpec, len(w.spawnGens))
	copy(out, w.spawnGens)
	return out
}

// ResultStep returns the WorkStep designated as the Node's typed
// output via SetResult, or nil if no typed output.
func (w *Work) ResultStep() *WorkStep { return w.resultStep }

// ResultType returns the Go type of the Work's typed output, or nil
// if no typed output.
func (w *Work) ResultType() reflect.Type { return w.resultType }

// SetResult marks step as the WorkStep whose return value is the
// Node's output. Only typed Steps (created via sparkwing.Out) carry
// a result type; passing a non-typed WorkStep is a plan-time error.
func (w *Work) SetResult(step *WorkStep) *Work {
	if step == nil {
		panic("sparkwing: Work.SetResult: step must be non-nil")
	}
	if step.outType == nil {
		panic(fmt.Sprintf("sparkwing: Work.SetResult: step %q has no typed output (use sparkwing.Out to declare a typed step)", step.id))
	}
	w.resultStep = step
	w.resultType = step.outType
	return w
}

// Step registers an inline closure as a unit of work inside this Work.
// Steps are executed in dependency order (Needs); independent steps
// may run in parallel.
//
//	w := sw.NewWork()
//	fetch := w.Step("fetch", j.fetch)
//	w.Step("validate", j.validate).Needs(fetch)
func (w *Work) Step(id string, fn func(ctx context.Context) error) *WorkStep {
	if id == "" {
		panic("sparkwing: Work.Step: id must not be empty")
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	if _, ok := w.byID[id]; ok {
		panic(fmt.Sprintf("sparkwing: Work.Step: duplicate step id %q", id))
	}
	s := &WorkStep{
		id: id,
		fn: func(ctx context.Context) (any, error) { return nil, fn(ctx) },
	}
	w.steps = append(w.steps, s)
	w.byID[id] = s
	return s
}

// Out registers a typed-output WorkStep on w. The returned
// *TypedStep[T] supports .Get(ctx) for downstream Steps.
//
//	tags := sparkwing.Out(w, "compute-tags", func(ctx context.Context) (Tags, error) { ... })
//	w.Step("publish", func(ctx context.Context) error {
//	    t := tags.Get(ctx)
//	    return publish(ctx, t)
//	}).Needs(tags.WorkStep)
//
// For the common case where a Work has exactly one typed step and
// that step IS the Node's typed output, prefer the Result[T] shortcut.
func Out[T any](w *Work, id string, fn func(ctx context.Context) (T, error)) *TypedStep[T] {
	if id == "" {
		panic("sparkwing: Out: id must not be empty")
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	if _, ok := w.byID[id]; ok {
		panic(fmt.Sprintf("sparkwing: Out: duplicate step id %q", id))
	}
	var zero T
	s := &WorkStep{
		id:      id,
		outType: reflect.TypeOf(zero),
		fn: func(ctx context.Context) (any, error) {
			v, err := fn(ctx)
			if err != nil {
				return nil, err
			}
			return v, nil
		},
	}
	w.steps = append(w.steps, s)
	w.byID[id] = s
	return &TypedStep[T]{WorkStep: s}
}

// Result is a shortcut for the common case where a typed Job's Work
// has exactly one typed step and that step IS the Job's result.
// Equivalent to Out followed by w.SetResult.
//
//	func (j *MyJob) Work() *sparkwing.Work {
//	    w := sparkwing.NewWork()
//	    sparkwing.Result(w, "run", j.run)
//	    return w
//	}
func Result[T any](w *Work, id string, fn func(ctx context.Context) (T, error)) *TypedStep[T] {
	step := Out(w, id, fn)
	w.SetResult(step.WorkStep)
	return step
}

// Sequence wires Needs edges between consecutive steps so each depends
// on the previous one. Returns the terminal step so the caller can
// chain further .Needs or modifiers on the endpoint.
func (w *Work) Sequence(steps ...*WorkStep) *WorkStep {
	for i := 1; i < len(steps); i++ {
		steps[i].Needs(steps[i-1])
	}
	if len(steps) == 0 {
		return nil
	}
	return steps[len(steps)-1]
}

// Parallel groups steps that should run concurrently. The returned
// StepGroup can be passed to a downstream step's Needs for fan-in.
func (w *Work) Parallel(steps ...*WorkStep) *StepGroup {
	return &StepGroup{members: steps}
}

// SpawnNode dispatches a registered Job as a fresh Plan node from
// inside a Work. The spawning runner suspends until the spawned node
// completes. Use sparingly: a suspended runner holds a slot of
// compute.
//
// The returned *SpawnHandle accepts .Needs to declare which Steps must
// complete before the spawn fires, and .Get(ctx) for typed output.
func (w *Work) SpawnNode(id string, job Workable) *SpawnHandle {
	if id == "" {
		panic("sparkwing: Work.SpawnNode: id must not be empty")
	}
	if job == nil {
		panic(fmt.Sprintf("sparkwing: Work.SpawnNode(%q): job must be non-nil", id))
	}
	spec := &SpawnSpec{
		id:  id,
		job: job,
	}
	w.spawns = append(w.spawns, spec)
	return &SpawnHandle{spec: spec}
}

// SpawnNodeForEach is the cardinality-many variant of SpawnNode. The
// generator runs once after the Spawn's Needs are satisfied; each
// returned (id, job) pair becomes a fresh Plan node dispatched in
// parallel. The spawning runner suspends across the entire fan-out.
func (w *Work) SpawnNodeForEach(items any, fn any) *SpawnGroup {
	if items == nil {
		panic("sparkwing: Work.SpawnNodeForEach: items must be non-nil")
	}
	if fn == nil {
		panic("sparkwing: Work.SpawnNodeForEach: fn must be non-nil")
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	spec := &SpawnGenSpec{
		// Synthetic id keyed by ordinal so downstream .Needs(group)
		// has a stable scheduling target.
		id:    fmt.Sprintf("__spawn_each_%d", len(w.spawnGens)),
		items: items,
		fn:    fn,
	}
	w.spawnGens = append(w.spawnGens, spec)
	return &SpawnGroup{spec: spec}
}

// WorkStep is one unit of work inside a Work. Steps are not Jobs;
// they run inside the Node's runner process and share its filesystem,
// environment, and ctx. Node-only modifiers (Retry, Timeout, OnFailure,
// Cache, RunsOn, BeforeRun/AfterRun) are deliberately absent on
// WorkStep -- promote to a Node via SpawnNode if you need them.
type WorkStep struct {
	id      string
	fn      func(ctx context.Context) (any, error)
	outType reflect.Type
	needs   []string
	skipIf  []SkipPredicate

	mu       sync.Mutex
	resolved bool
	out      any
	done     chan struct{}
}

// ID returns the step's identifier.
func (s *WorkStep) ID() string { return s.id }

// OutputType returns the typed output reflect.Type, or nil for steps
// that return only error.
func (s *WorkStep) OutputType() reflect.Type { return s.outType }

// Fn returns the underlying executable closure. Intended for the
// orchestrator.
func (s *WorkStep) Fn() func(ctx context.Context) (any, error) { return s.fn }

// Needs declares hard upstream Step / Spawn dependencies inside the
// same Work. Accepts *WorkStep, *TypedStep[T] (via embedded
// *WorkStep), *StepGroup, *SpawnHandle, *SpawnGroup, or string IDs.
func (s *WorkStep) Needs(deps ...any) *WorkStep {
	for _, d := range deps {
		coerceDep(d, "WorkStep.Needs", &s.needs)
	}
	return s
}

func coerceDep(d any, caller string, out *[]string) {
	add := func(id string) {
		if id == "" {
			return
		}
		if !slices.Contains(*out, id) {
			*out = append(*out, id)
		}
	}
	switch v := d.(type) {
	case *WorkStep:
		if v != nil {
			add(v.id)
		}
	case *StepGroup:
		if v != nil {
			for _, m := range v.members {
				add(m.id)
			}
		}
	case *SpawnHandle:
		if v != nil && v.spec != nil {
			add(v.spec.id)
		}
	case *SpawnGroup:
		// Fan-in waits on the generator's synthetic id; member ids
		// aren't known until the generator runs.
		if v != nil && v.spec != nil {
			add(v.spec.syntheticID())
		}
	case string:
		add(v)
	case []*WorkStep:
		for _, vv := range v {
			if vv != nil {
				add(vv.id)
			}
		}
	default:
		if step := unwrapStep(d); step != nil {
			add(step.id)
			return
		}
		panic(fmt.Sprintf("sparkwing: %s: unsupported dep type %T", caller, d))
	}
}

// DepIDs returns the step IDs this step depends on.
func (s *WorkStep) DepIDs() []string {
	out := make([]string, len(s.needs))
	copy(out, s.needs)
	return out
}

func (s *WorkStep) addNeed(id string) {
	if slices.Contains(s.needs, id) {
		return
	}
	s.needs = append(s.needs, id)
}

// SkipIf registers a predicate the runner evaluates after this step's
// upstream deps complete. Multiple SkipIf calls accumulate with OR
// semantics.
func (s *WorkStep) SkipIf(fn SkipPredicate) *WorkStep {
	if fn != nil {
		s.skipIf = append(s.skipIf, fn)
	}
	return s
}

// SkipPredicates returns the registered skip predicates.
func (s *WorkStep) SkipPredicates() []SkipPredicate { return s.skipIf }

// MarkDone is called by the runner once the step terminates. Stores
// the typed output so downstream *TypedStep[T].Get(ctx) calls resolve.
func (s *WorkStep) MarkDone(out any) {
	s.mu.Lock()
	if s.done == nil {
		s.done = make(chan struct{})
	}
	if s.resolved {
		s.mu.Unlock()
		return
	}
	s.resolved = true
	s.out = out
	close(s.done)
	s.mu.Unlock()
}

// Output returns the resolved typed output (after MarkDone) or nil.
func (s *WorkStep) Output() any {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.out
}

func (s *WorkStep) awaitDone(ctx context.Context) error {
	s.mu.Lock()
	if s.resolved {
		s.mu.Unlock()
		return nil
	}
	if s.done == nil {
		s.done = make(chan struct{})
	}
	ch := s.done
	s.mu.Unlock()
	select {
	case <-ch:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// TypedStep wraps a WorkStep with a typed Get(ctx) for downstream
// Steps. Returned by sparkwing.Out.
type TypedStep[T any] struct {
	*WorkStep
}

// Get blocks until the producing step completes, then returns its
// typed output. Panics if the step failed (the Work execution path
// already short-circuited; calling Get after failure is a programmer
// error).
func (t *TypedStep[T]) Get(ctx context.Context) T {
	if err := t.WorkStep.awaitDone(ctx); err != nil {
		var zero T
		panic(fmt.Sprintf("sparkwing: TypedStep[%T].Get: ctx done before step %q completed: %v", zero, t.WorkStep.id, err))
	}
	v := t.WorkStep.Output()
	if v == nil {
		var zero T
		return zero
	}
	typed, ok := v.(T)
	if !ok {
		var zero T
		panic(fmt.Sprintf("sparkwing: TypedStep[%T].Get: step %q produced %T, not assignable", zero, t.WorkStep.id, v))
	}
	return typed
}

// StepGroup is a handle to a static group of Steps. Returned by
// w.Parallel. Downstream .Needs(group) expands eagerly to the group's
// members.
type StepGroup struct {
	members []*WorkStep
}

// Members returns the group's steps.
func (g *StepGroup) Members() []*WorkStep {
	out := make([]*WorkStep, len(g.members))
	copy(out, g.members)
	return out
}

// SpawnSpec is the static record of a SpawnNode declaration. The
// orchestrator walks every Work's spawns at Plan-time and recursively
// materializes each target Job's own Work.
type SpawnSpec struct {
	id         string
	job        Workable
	needs      []string
	skipIf     []SkipPredicate
	resolvedID string

	mu       sync.Mutex
	resolved bool
	out      any
	done     chan struct{}
}

// ID returns the spawn's local id (not the eventual Plan node id, which
// is namespaced by the spawning Node).
func (s *SpawnSpec) ID() string { return s.id }

// Job returns the spawn's target.
func (s *SpawnSpec) Job() Workable { return s.job }

// DepIDs returns WorkStep IDs the spawn waits on inside its parent Work.
func (s *SpawnSpec) DepIDs() []string {
	out := make([]string, len(s.needs))
	copy(out, s.needs)
	return out
}

// SkipPredicates returns the spawn's registered predicates.
func (s *SpawnSpec) SkipPredicates() []SkipPredicate { return s.skipIf }

// SetResolvedID records the namespaced Plan node id assigned when the
// spawn fires. Set by the orchestrator.
func (s *SpawnSpec) SetResolvedID(id string) { s.resolvedID = id }

// ResolvedID returns the assigned Plan node id, populated after the
// spawn fires. Empty before then.
func (s *SpawnSpec) ResolvedID() string { return s.resolvedID }

// MarkDone is called by the orchestrator once the spawned node
// terminates so SpawnHandle.Get can resolve.
func (s *SpawnSpec) MarkDone(out any) {
	s.mu.Lock()
	if s.done == nil {
		s.done = make(chan struct{})
	}
	if s.resolved {
		s.mu.Unlock()
		return
	}
	s.resolved = true
	s.out = out
	close(s.done)
	s.mu.Unlock()
}

func (s *SpawnSpec) awaitDone(ctx context.Context) error {
	s.mu.Lock()
	if s.resolved {
		s.mu.Unlock()
		return nil
	}
	if s.done == nil {
		s.done = make(chan struct{})
	}
	ch := s.done
	s.mu.Unlock()
	select {
	case <-ch:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// SpawnHandle is the author-facing handle to a SpawnNode declaration.
type SpawnHandle struct {
	spec *SpawnSpec
}

// Spec returns the underlying SpawnSpec. Intended for the orchestrator.
func (h *SpawnHandle) Spec() *SpawnSpec { return h.spec }

// Needs declares which Steps / Spawns inside the same Work must
// complete before the spawn fires. Mirrors WorkStep.Needs at the
// spawn layer.
func (h *SpawnHandle) Needs(deps ...any) *SpawnHandle {
	for _, d := range deps {
		coerceDep(d, "SpawnHandle.Needs", &h.spec.needs)
	}
	return h
}

// SkipIf registers a predicate the orchestrator evaluates before
// firing the spawn.
func (h *SpawnHandle) SkipIf(fn SkipPredicate) *SpawnHandle {
	if fn != nil {
		h.spec.skipIf = append(h.spec.skipIf, fn)
	}
	return h
}

// SpawnGenSpec is the static record of a SpawnNodeForEach declaration.
// The generator runs at dispatch time once Needs are satisfied.
type SpawnGenSpec struct {
	id    string
	items any
	fn    any
	needs []string
}

// syntheticID returns the scheduling-id for a SpawnNodeForEach
// fan-out group.
func (g *SpawnGenSpec) syntheticID() string { return g.id }

// ID exposes the synthetic id (e.g. "__spawn_each_0") to renderers
// and the orchestrator's snapshot walker.
func (g *SpawnGenSpec) ID() string { return g.id }

// Items returns the input slice value.
func (g *SpawnGenSpec) Items() any { return g.items }

// Fn returns the per-item closure. Reflection-typed; closure shape
// is func(T) (string, Workable).
func (g *SpawnGenSpec) Fn() any { return g.fn }

// DepIDs returns the WorkStep IDs the generator waits on.
func (g *SpawnGenSpec) DepIDs() []string {
	out := make([]string, len(g.needs))
	copy(out, g.needs)
	return out
}

// SpawnGroup is the author-facing handle returned by SpawnNodeForEach.
type SpawnGroup struct {
	spec *SpawnGenSpec
}

// Spec returns the underlying SpawnGenSpec.
func (g *SpawnGroup) Spec() *SpawnGenSpec { return g.spec }

// Needs declares which Steps / Spawns must complete before the
// generator runs.
func (g *SpawnGroup) Needs(deps ...any) *SpawnGroup {
	for _, d := range deps {
		coerceDep(d, "SpawnGroup.Needs", &g.spec.needs)
	}
	return g
}

// unwrapStep extracts an embedded *WorkStep from typed wrappers like
// *TypedStep[T] via reflection. Returns nil if none found.
func unwrapStep(v any) *WorkStep {
	rv := reflect.ValueOf(v)
	if !rv.IsValid() {
		return nil
	}
	for rv.Kind() == reflect.Pointer {
		if rv.IsNil() {
			return nil
		}
		rv = rv.Elem()
	}
	if rv.Kind() != reflect.Struct {
		return nil
	}
	for i := range rv.NumField() {
		fv := rv.Field(i)
		if !fv.IsValid() {
			continue
		}
		if fv.Kind() == reflect.Pointer && fv.Type() == reflect.TypeFor[*WorkStep]() {
			if fv.IsNil() {
				return nil
			}
			return fv.Interface().(*WorkStep)
		}
	}
	return nil
}

// JobFn wraps a closure as a single-step Job:
//
//	plan.Node("test", sparkwing.JobFn(func(ctx context.Context) error {
//	    _, err := sparkwing.Bash(ctx, "go test ./...").Run()
//	    return err
//	}))
func JobFn(fn func(ctx context.Context) error) Workable {
	if fn == nil {
		panic("sparkwing: JobFn: fn must be non-nil")
	}
	return &jobFn{fn: fn}
}

type jobFn struct {
	fn func(ctx context.Context) error
}

func (c *jobFn) Work() *Work {
	w := NewWork()
	w.Step("run", c.fn)
	return w
}
