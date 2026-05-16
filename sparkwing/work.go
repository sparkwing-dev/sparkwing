package sparkwing

import (
	"context"
	"fmt"
	"reflect"
	"slices"
	"sync"
)

// Workable is the interface every dispatchable Job satisfies: a struct
// that exposes its inner DAG via Work(w). The SDK constructs the
// *Work and passes it in; the author registers steps onto w and
// returns the *WorkStep designated as the Job's typed output (or
// nil for an untyped Job). A non-nil error fails Plan-time
// materialization. Author types embed sparkwing.Base (and
// optionally sparkwing.Produces[T]); the SDK materializes the inner
// Work at Plan-time so renderers see the full graph before any
// dispatch begins.
//
// For the trivial single-step case pass a func(ctx) error directly to
// sparkwing.Job; for typed jobs declare a struct embedding
// sparkwing.Produces[T] and return the typed step's *WorkStep from
// Work.
type Workable interface {
	Work(w *Work) (*WorkStep, error)
}

// Work is the inner DAG of a Job. Mirrors Plan at the inner layer:
// Steps with Needs / SkipIf, plus Sequence / Parallel combinators and
// Spawn primitives for layer escape.
//
// Build via NewWork. The orchestrator calls Job.Work() once per Job
// at Plan-time and walks the entire reachable graph (including Spawn
// targets) before any dispatch.
type Work struct {
	mu        sync.Mutex
	steps     []*WorkStep
	byID      map[string]*WorkStep
	spawns    []*SpawnSpec
	spawnGens []*SpawnGenSpec
	groups    []*StepGroup
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

// Spawns returns the static JobSpawn declarations registered on
// this Work.
func (w *Work) Spawns() []*SpawnSpec {
	out := make([]*SpawnSpec, len(w.spawns))
	copy(out, w.spawns)
	return out
}

// SpawnGens returns the JobSpawnEach declarations. Shape is known at
// Plan-time; cardinality is decided at dispatch.
func (w *Work) SpawnGens() []*SpawnGenSpec {
	out := make([]*SpawnGenSpec, len(w.spawnGens))
	copy(out, w.spawnGens)
	return out
}

// Groups returns the StepGroups declared on this Work in declaration
// order. Each entry is a (name, members) bundle the plan-snapshot
// walker surfaces to the dashboard so it can frame group members.
func (w *Work) Groups() []*StepGroup {
	w.mu.Lock()
	defer w.mu.Unlock()
	out := make([]*StepGroup, len(w.groups))
	copy(out, w.groups)
	return out
}

// Step registers a unit of work on this Work. fn must be either
//
//	func(ctx context.Context) error              -- untyped step
//	func(ctx context.Context) (T, error)         -- typed step
//
// for some concrete T. Reflection at register time validates the
// signature and stores the step's typed-output reflect.Type (nil for
// untyped). A wrong-shape fn panics with a typed message at register
// time, mirroring JobSpawnEach's plan-time validation.
//
// Authors compose typed-step values inside another step body via
// sparkwing.StepGet[T](ctx, step). The *WorkStep returned for a typed
// step is the same value the Job's Work returns to mark its typed
// output; the materializer cross-validates that returned step's
// outType against any Produces[T] marker on the Job.
//
//	fetch := sparkwing.Step(w, "fetch", j.fetch)
//	sparkwing.Step(w, "validate", j.validate).Needs(fetch)
//
//	tags := sparkwing.Step(w, "tags", j.computeTags) // (Tags, error)
//	return sparkwing.Step(w, "compose", func(ctx context.Context) (Out, error) {
//	    return Out{Tag: sw.StepGet[Tags](ctx, tags)}, nil
//	}).Needs(tags), nil
func Step(w *Work, id string, fn any) *WorkStep {
	if w == nil {
		panic("sparkwing: Step: w must be non-nil")
	}
	if id == "" {
		panic("sparkwing: Step: id must not be empty")
	}
	outT, dispatch := validateStepFn(fn)
	w.mu.Lock()
	defer w.mu.Unlock()
	if _, ok := w.byID[id]; ok {
		panic(fmt.Sprintf("sparkwing: Step: duplicate step id %q", id))
	}
	s := &WorkStep{
		id:      id,
		outType: outT,
		fn:      dispatch,
	}
	w.steps = append(w.steps, s)
	w.byID[id] = s
	return s
}

// validateStepFn checks Step's reflective contract at register time
// so a bad fn signature panics during Work construction rather than
// at dispatch. Returns the typed output reflect.Type (nil for untyped
// steps) and the unified dispatch closure the runner invokes.
func validateStepFn(fn any) (reflect.Type, func(ctx context.Context) (any, error)) {
	if fn == nil {
		panic("sparkwing: Step: fn must be non-nil")
	}
	// Fast paths for the two concrete signatures avoid reflection at
	// dispatch-time for the common cases.
	switch f := fn.(type) {
	case func(ctx context.Context) error:
		return nil, func(ctx context.Context) (any, error) { return nil, f(ctx) }
	}

	fnT := reflect.TypeOf(fn)
	if fnT.Kind() != reflect.Func {
		panic(fmt.Sprintf("sparkwing: Step: fn must be a func, got %T", fn))
	}
	if fnT.IsVariadic() {
		panic(fmt.Sprintf("sparkwing: Step: fn must not be variadic (signature: %v)", fnT))
	}
	if fnT.NumIn() != 1 {
		panic(fmt.Sprintf(
			"sparkwing: Step: fn must take exactly 1 argument (context.Context), got %d (signature: %v)",
			fnT.NumIn(), fnT))
	}
	ctxT := reflect.TypeOf((*context.Context)(nil)).Elem()
	if fnT.In(0) != ctxT {
		panic(fmt.Sprintf(
			"sparkwing: Step: fn argument must be context.Context, got %v",
			fnT.In(0)))
	}
	errT := reflect.TypeOf((*error)(nil)).Elem()
	switch fnT.NumOut() {
	case 1:
		if fnT.Out(0) != errT {
			panic(fmt.Sprintf(
				"sparkwing: Step: fn with one return value must return error, got %v "+
					"(want func(context.Context) error or func(context.Context) (T, error))",
				fnT.Out(0)))
		}
		fnv := reflect.ValueOf(fn)
		return nil, func(ctx context.Context) (any, error) {
			out := fnv.Call([]reflect.Value{reflect.ValueOf(ctx)})
			if e := out[0].Interface(); e != nil {
				return nil, e.(error)
			}
			return nil, nil
		}
	case 2:
		if fnT.Out(1) != errT {
			panic(fmt.Sprintf(
				"sparkwing: Step: fn second return value must be error, got %v "+
					"(want func(context.Context) (T, error))",
				fnT.Out(1)))
		}
		outT := fnT.Out(0)
		fnv := reflect.ValueOf(fn)
		return outT, func(ctx context.Context) (any, error) {
			out := fnv.Call([]reflect.Value{reflect.ValueOf(ctx)})
			var err error
			if e := out[1].Interface(); e != nil {
				err = e.(error)
			}
			if err != nil {
				return nil, err
			}
			return out[0].Interface(), nil
		}
	default:
		panic(fmt.Sprintf(
			"sparkwing: Step: fn must return error or (T, error), got %d return values (signature: %v)",
			fnT.NumOut(), fnT))
	}
}

// StepGet blocks until step has completed, then returns its typed
// output as T. Used inside another step's body when composing values
// from upstream typed steps. Mirrors Plan's sparkwing.RefTo[T](node).Get(ctx).
//
// Panics on:
//   - nil step
//   - step with no typed output (registered with func(ctx) error)
//   - step's typed output type doesn't match T
//   - ctx cancelled before step's MarkDone fires
func StepGet[T any](ctx context.Context, step *WorkStep) T {
	var zero T
	if step == nil {
		panic("sparkwing: StepGet: step must be non-nil")
	}
	wantT := reflect.TypeOf(zero)
	if step.outType == nil {
		panic(fmt.Sprintf(
			"sparkwing: StepGet[%v]: step %q has no typed output "+
				"(register it with func(ctx) (T, error) to enable StepGet)",
			wantT, step.id))
	}
	if step.outType != wantT {
		panic(fmt.Sprintf(
			"sparkwing: StepGet[%v]: step %q produces %v, not %v",
			wantT, step.id, step.outType, wantT))
	}
	if err := step.awaitDone(ctx); err != nil {
		panic(fmt.Sprintf(
			"sparkwing: StepGet[%v]: ctx done before step %q completed: %v",
			wantT, step.id, err))
	}
	v := step.Output()
	if v == nil {
		return zero
	}
	typed, ok := v.(T)
	if !ok {
		panic(fmt.Sprintf(
			"sparkwing: StepGet[%v]: step %q produced %T, not assignable",
			wantT, step.id, v))
	}
	return typed
}

// JobSpawn dispatches a registered Job as a fresh Plan node from
// inside a Work. The spawning runner suspends until the spawned node
// completes. Use sparingly: a suspended runner holds a slot of
// compute.
//
// The returned *SpawnHandle accepts .Needs to declare which Steps must
// complete before the spawn fires, and .Get(ctx) for typed output.
//
// "Spawn" is a lifecycle suffix here -- the verb adds a Plan Job
// from inside Work, hence the Job- prefix.
//
// Accepts the same argument shapes as sparkwing.Job's third arg
// (Workable struct or func(ctx) error closure).
func JobSpawn(w *Work, id string, x any) *SpawnHandle {
	if w == nil {
		panic("sparkwing: JobSpawn: w must be non-nil")
	}
	if id == "" {
		panic("sparkwing: JobSpawn: id must not be empty")
	}
	job := coerceJobArg("JobSpawn", id, x)
	spec := &SpawnSpec{
		id:  id,
		job: job,
	}
	w.spawns = append(w.spawns, spec)
	return &SpawnHandle{spec: spec}
}

// JobSpawnEach is the cardinality-many variant of JobSpawn. The
// generator runs once after the Spawn's Needs are satisfied; each
// returned (id, job) pair becomes a fresh Plan node dispatched in
// parallel. The spawning runner suspends across the entire fan-out.
//
// items must be a slice (or array). fn must be a func of shape
//
//	func(T) (string, sparkwing.Workable)
//
// where T is assignable from items's element type. Both shapes are
// validated at Plan time via reflection so a wrong-shaped fn panics
// alongside other structural errors (Produces/Work-return mismatch,
// duplicate IDs) rather than blowing up later during dispatch.
func JobSpawnEach(w *Work, items any, fn any) *SpawnGroup {
	if w == nil {
		panic("sparkwing: JobSpawnEach: w must be non-nil")
	}
	validateSpawnEach(items, fn)
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

// validateSpawnEach checks JobSpawnEach's reflective contract at
// Plan time so a bad fn signature panics during plan construction
// rather than at dispatch -- matches how every other structural
// SDK error (Produces/Work-return mismatch, duplicate IDs, invalid
// Approval.OnExpiry) surfaces.
func validateSpawnEach(items any, fn any) {
	if items == nil {
		panic("sparkwing: JobSpawnEach: items must be non-nil")
	}
	if fn == nil {
		panic("sparkwing: JobSpawnEach: fn must be non-nil")
	}
	itemsT := reflect.TypeOf(items)
	if k := itemsT.Kind(); k != reflect.Slice && k != reflect.Array {
		panic(fmt.Sprintf("sparkwing: JobSpawnEach: items must be a slice or array, got %T", items))
	}
	fnT := reflect.TypeOf(fn)
	if fnT.Kind() != reflect.Func {
		panic(fmt.Sprintf("sparkwing: JobSpawnEach: fn must be a func, got %T", fn))
	}
	if fnT.NumIn() != 1 {
		panic(fmt.Sprintf(
			"sparkwing: JobSpawnEach: fn must take exactly 1 argument (the item), got %d (signature: %v)",
			fnT.NumIn(), fnT))
	}
	if fnT.NumOut() != 2 {
		panic(fmt.Sprintf(
			"sparkwing: JobSpawnEach: fn must return (string, sparkwing.Workable) "+
				"or (string, func(ctx context.Context) error), "+
				"got %d return values (signature: %v)",
			fnT.NumOut(), fnT))
	}
	elemT := itemsT.Elem()
	argT := fnT.In(0)
	if !elemT.AssignableTo(argT) {
		panic(fmt.Sprintf(
			"sparkwing: JobSpawnEach: fn argument type %v is not assignable from items element type %v",
			argT, elemT))
	}
	stringT := reflect.TypeOf("")
	if fnT.Out(0) != stringT {
		panic(fmt.Sprintf(
			"sparkwing: JobSpawnEach: fn first return value must be string, got %v",
			fnT.Out(0)))
	}
	workableT := reflect.TypeOf((*Workable)(nil)).Elem()
	closureT := reflect.TypeOf((func(context.Context) error)(nil))
	emptyIfaceT := reflect.TypeOf((*any)(nil)).Elem()
	out1 := fnT.Out(1)
	// Accept Workable, func(ctx) error, or any. The any case defers
	// the runtime check to coerceSpawnEachJob, mirroring how
	// sparkwing.Job handles its `any` third arg.
	if !out1.Implements(workableT) && out1 != closureT && out1 != emptyIfaceT {
		panic(fmt.Sprintf(
			"sparkwing: JobSpawnEach: fn second return value must be sparkwing.Workable "+
				"or func(ctx context.Context) error, got %v",
			out1))
	}
}

// CoerceSpawnEachJob normalizes the second-return of a JobSpawnEach
// per-item callback into a Workable. Mirrors coerceJobArg for the
// fan-out case so closure-form jobs work uniformly without an
// explicit wrapper. Exported so the orchestrator's template
// materializer can apply the same shape rules at dispatch time.
func CoerceSpawnEachJob(v any) (Workable, error) {
	switch j := v.(type) {
	case Workable:
		return j, nil
	case func(ctx context.Context) error:
		return &jobFn{fn: j}, nil
	}
	rv := reflect.ValueOf(v)
	if rv.IsValid() && rv.Type().Implements(reflect.TypeOf((*Workable)(nil)).Elem()) {
		return rv.Interface().(Workable), nil
	}
	return nil, fmt.Errorf("sparkwing: JobSpawnEach: per-item job has unsupported type %T", v)
}

// coerceSpawnEachJob is the panic-on-error variant used inside the
// runner where a structurally-valid spec is already guaranteed by
// validateSpawnEach.
func coerceSpawnEachJob(v any) Workable {
	job, err := CoerceSpawnEachJob(v)
	if err != nil {
		panic(err.Error())
	}
	return job
}

// WorkStep is one unit of work inside a Work. Steps are not Jobs;
// they run inside the Job's runner process and share its filesystem,
// environment, and ctx. Job-only modifiers (Retry, Timeout, OnFailure,
// Cache, Requires, BeforeRun/AfterRun) are deliberately absent on
// WorkStep -- promote to a Job via JobSpawn if you need them.
type WorkStep struct {
	id              string
	fn              func(ctx context.Context) (any, error)
	outType         reflect.Type
	needs           []string
	skipIf          []SkipPredicate
	continueOnError bool
	optional        bool
	// Dry-run contract. dryRunFn is installed via
	// .DryRun(fn) and runs in place of fn when the orchestrator
	// dispatches under WithDryRun(ctx). safeWithoutDryRun is the
	// explicit "this step has no side effects" marker that lets fn
	// execute under --dry-run unmodified.
	dryRunFn          func(ctx context.Context) error
	safeWithoutDryRun bool
	// Author-declared blast-radius markers. The dispatcher
	// walks this set per step against the wing-level --allow-*
	// flags (and bypasses entirely under --dry-run). An empty set
	// means "no declared blast radius" -- the gate doesn't fire,
	// preserving zero-behavior-change for existing pipelines.
	blastRadius []BlastRadius

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
// same Work. Accepts *WorkStep, *StepGroup, *SpawnHandle, *SpawnGroup,
// or string IDs.
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

// ContinueOnError marks the step's failure as non-blocking for the
// rest of the Work: in-flight sibling steps are not cancelled, and
// downstream steps that .Needs() this one still dispatch. The Job's
// overall outcome still reports the failure unless the step is also
// marked Optional. Mirrors Job.ContinueOnError at the Plan layer.
//
//	a := sw.Step(w, "scan-a", scanA).ContinueOnError()
//	b := sw.Step(w, "scan-b", scanB).ContinueOnError() // runs even if a fails
//	sw.Step(w, "report", report).Needs(a, b)            // runs even if both fail
func (s *WorkStep) ContinueOnError() *WorkStep {
	s.continueOnError = true
	return s
}

// IsContinueOnError reports whether this step's failure is non-
// blocking for sibling cancellation and downstream Needs() dispatch.
func (s *WorkStep) IsContinueOnError() bool { return s.continueOnError }

// Optional marks the step as non-essential: a failure is recorded
// (still visible in logs and step status) but does not count toward
// the Job's rollup outcome. Implies ContinueOnError. Mirrors
// Job.Optional at the Plan layer.
//
//	sw.Step(w, "best-effort-metrics", emitMetrics).Optional()
func (s *WorkStep) Optional() *WorkStep {
	s.optional = true
	s.continueOnError = true
	return s
}

// IsOptional reports whether this step's failure is masked from the
// Job's rollup outcome.
func (s *WorkStep) IsOptional() bool { return s.optional }

// MarkDone is called by the runner once the step terminates. Stores
// the typed output so downstream sparkwing.StepGet[T](ctx, step) calls
// resolve.
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

// StepGroup is a handle to a named group of Steps. Returned by
// sparkwing.GroupSteps. Downstream .Needs(group) expands eagerly to
// the group's members. Modifiers (Needs, SkipIf) delegate to every
// member, mirroring the *WorkStep modifier surface so future
// step-level modifiers can be added uniformly to both.
type StepGroup struct {
	name    string
	members []*WorkStep
}

// Name returns the group's declared name. The dashboard's Work view
// renders the cluster under this name; an empty name means
// "structural collection only" (no UI cluster).
func (g *StepGroup) Name() string { return g.name }

// Members returns the group's steps.
func (g *StepGroup) Members() []*WorkStep {
	out := make([]*WorkStep, len(g.members))
	copy(out, g.members)
	return out
}

// GroupSteps declares a named bundle of Work steps. The returned
// *StepGroup is both a Needs target (downstream depends on every
// member) and a dashboard cluster (rendered as a single visual unit
// under the given name; empty name = structural collection only).
//
//	fetch := sw.Step(w, "fetch", j.fetch)
//	checks := sw.GroupSteps(w, "safety",
//	    sw.Step(w, "lint",    j.lint).Needs(fetch),
//	    sw.Step(w, "secscan", j.secscan).Needs(fetch),
//	    sw.Step(w, "vet",     j.vet).Needs(fetch),
//	)
//	return sw.Step(w, "deploy", j.deploy).Needs(checks), nil
//
// The mirror of sparkwing.GroupJobs at the Work layer.
func GroupSteps(w *Work, name string, steps ...*WorkStep) *StepGroup {
	if w == nil {
		panic("sparkwing: GroupSteps: w must be non-nil")
	}
	members := make([]*WorkStep, 0, len(steps))
	for _, s := range steps {
		if s != nil {
			members = append(members, s)
		}
	}
	g := &StepGroup{name: name, members: members}
	w.mu.Lock()
	w.groups = append(w.groups, g)
	w.mu.Unlock()
	return g
}

// Needs declares an upstream dependency on every member of the group.
// Accepts the same shapes as WorkStep.Needs.
func (g *StepGroup) Needs(deps ...any) *StepGroup {
	for _, m := range g.members {
		m.Needs(deps...)
	}
	return g
}

// SkipIf registers a predicate on every member of the group. See
// WorkStep.SkipIf.
func (g *StepGroup) SkipIf(fn SkipPredicate) *StepGroup {
	if fn == nil {
		return g
	}
	for _, m := range g.members {
		m.SkipIf(fn)
	}
	return g
}

// SpawnSpec is the static record of a JobSpawn declaration. The
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
// is namespaced by the spawning Job).
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

// SpawnHandle is the author-facing handle to a JobSpawn declaration.
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

// SpawnGenSpec is the static record of a JobSpawnEach declaration.
// The generator runs at dispatch time once Needs are satisfied.
type SpawnGenSpec struct {
	id    string
	items any
	fn    any
	needs []string
}

// syntheticID returns the scheduling-id for a JobSpawnEach
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

// SpawnGroup is the author-facing handle returned by JobSpawnEach.
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

// unwrapStep extracts an embedded *WorkStep from typed wrappers via
// reflection. Returns nil if none found.
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

// jobFn is the unexported Workable wrapper used internally when
// sparkwing.Job receives a func(ctx) error directly. Pipeline authors
// don't construct it -- pass the closure to sparkwing.Job and the
// plan-time wrapper installs it transparently.
type jobFn struct {
	fn func(ctx context.Context) error
}

func (c *jobFn) Work(w *Work) (*WorkStep, error) {
	Step(w, "run", c.fn)
	return nil, nil
}
