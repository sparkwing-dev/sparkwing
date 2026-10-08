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
// [Work] and passes it in; the author registers steps onto w via
// [Step] and returns the [WorkStep] designated as the Job's typed
// output (or nil for an untyped Job). A non-nil error fails Plan-time
// materialization. Author types embed [Base] (and optionally
// [Produces][T]); the SDK materializes the inner Work at Plan time
// so renderers see the full graph before any dispatch begins.
//
// For the trivial single-step case pass a func(ctx) error directly to
// [Job]; for typed jobs declare a struct embedding [Produces][T] and
// return the typed step's *WorkStep from Work.
type Workable interface {
	Work(w *Work) (*WorkStep, error)
}

// Work is the inner DAG of a Job. Mirrors [Plan] at the inner layer:
// [WorkStep]s with [WorkStep.Needs] / [WorkStep.SkipIf], plus
// [GroupSteps] for named bundles.
//
// Build via [NewWork]. The orchestrator calls [Workable.Work] once
// per Job at Plan time and walks the step graph before any dispatch.
type Work struct {
	mu       sync.Mutex
	steps    []*WorkStep
	byID     map[string]*WorkStep
	groups   []*StepGroup
	failures ParallelFailurePolicy
}

// ParallelFailurePolicy controls whether independent Work items finish after
// one of their siblings fails.
type ParallelFailurePolicy string

const (
	// FailFast cancels ordinary in-flight siblings after the first decisive
	// failure. It is the default and preserves the historical Work behavior.
	FailFast ParallelFailurePolicy = "fail-fast"
	// CollectAll lets every independent or already-ready item finish so one run
	// can report the full failure set. It does not satisfy a failed prerequisite;
	// downstream Needs still require success or an explicit ContinueOnError.
	CollectAll ParallelFailurePolicy = "collect-all"
)

// NewWork returns an empty Work.
func NewWork() *Work {
	return &Work{byID: map[string]*WorkStep{}}
}

// ParallelFailures sets the policy for independent items in this Work.
func (w *Work) ParallelFailures(policy ParallelFailurePolicy) *Work {
	switch policy {
	case FailFast, CollectAll:
		w.failures = policy
	default:
		panic(fmt.Sprintf("sparkwing: Work.ParallelFailures: unsupported policy %q", policy))
	}
	return w
}

// ParallelFailurePolicy returns the configured policy. The zero value is
// [FailFast] for backward compatibility.
func (w *Work) ParallelFailurePolicy() ParallelFailurePolicy {
	if w == nil || w.failures == "" {
		return FailFast
	}
	return w.failures
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
// time.
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

func validateStepFn(fn any) (reflect.Type, func(ctx context.Context) (any, error)) {
	if fn == nil {
		panic("sparkwing: Step: fn must be non-nil")
	}
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
			fnT.NumIn(), fnT,
		))
	}
	ctxT := reflect.TypeOf((*context.Context)(nil)).Elem()
	if fnT.In(0) != ctxT {
		panic(fmt.Sprintf(
			"sparkwing: Step: fn argument must be context.Context, got %v",
			fnT.In(0),
		))
	}
	errT := reflect.TypeOf((*error)(nil)).Elem()
	switch fnT.NumOut() {
	case 1:
		if fnT.Out(0) != errT {
			panic(fmt.Sprintf(
				"sparkwing: Step: fn with one return value must return error, got %v "+
					"(want func(context.Context) error or func(context.Context) (T, error))",
				fnT.Out(0),
			))
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
				fnT.Out(1),
			))
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
			fnT.NumOut(), fnT,
		))
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
//   - ctx cancelled before step completes
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
			wantT, step.id,
		))
	}
	if step.outType != wantT {
		panic(fmt.Sprintf(
			"sparkwing: StepGet[%v]: step %q produces %v, not %v",
			wantT, step.id, step.outType, wantT,
		))
	}
	if err := step.awaitDone(ctx); err != nil {
		panic(fmt.Sprintf(
			"sparkwing: StepGet[%v]: ctx done before step %q completed: %v",
			wantT, step.id, err,
		))
	}
	v := step.Output()
	if v == nil {
		return zero
	}
	typed, ok := v.(T)
	if !ok {
		panic(fmt.Sprintf(
			"sparkwing: StepGet[%v]: step %q produced %T, not assignable",
			wantT, step.id, v,
		))
	}
	return typed
}

// WorkStep is one unit of work inside a [Work]. Steps are not Jobs;
// they run inside the Job's runner process and share its filesystem,
// environment, and ctx. Returned by [Step]; modifier methods
// ([WorkStep.Needs], [WorkStep.SkipIf], [WorkStep.Risk],
// [WorkStep.DryRun], [WorkStep.SafeWithoutDryRun]) chain off it.
// Plan-layer modifiers (Retry, Timeout, OnFailure, Cache, Requires,
// BeforeRun / AfterRun) belong to a Plan-level [Job]; promote the
// step to one if you need them.
type WorkStep struct {
	id              string
	fn              func(ctx context.Context) (any, error)
	outType         reflect.Type
	needs           []string
	skipIf          []SkipPredicate
	continueOnError bool
	optional        bool
	finally         bool

	dryRunFn          func(ctx context.Context) error
	safeWithoutDryRun bool

	risks []string

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

// WorkDep is the closed type set accepted by Work-layer [WorkStep.Needs]
// and [StepGroup.Needs].
// The unexported marker method `workDepID()` prevents callers from
// passing arbitrary values; the Plan-layer [Dep] types are NOT WorkDep
// and vice versa, so the two layers cannot cross by accident.
//
// Implementations: [*WorkStep], [*StepGroup]. By-name references via a
// typed-string sentinel are not supported -- store and pass the
// upstream's handle.
type WorkDep interface {
	workDepID() string
}

func (s *WorkStep) workDepID() string { return s.id }
func (g *StepGroup) workDepID() string {
	return g.name
}

var (
	_ WorkDep = (*WorkStep)(nil)
	_ WorkDep = (*StepGroup)(nil)
)

// Needs declares hard upstream Step dependencies inside the same
// Work. Accepts any [WorkDep]: [*WorkStep] or [*StepGroup]. For
// multiple steps from a slice,
// splat: `s.Needs(steps...)`.
func (s *WorkStep) Needs(deps ...WorkDep) *WorkStep {
	for _, d := range deps {
		addWorkDep(d, &s.needs)
	}
	return s
}

func addWorkDep(d WorkDep, out *[]string) {
	if d == nil {
		return
	}
	add := func(id string) {
		if id == "" {
			return
		}
		if !slices.Contains(*out, id) {
			*out = append(*out, id)
		}
	}
	if g, ok := d.(*StepGroup); ok && g != nil {
		for _, m := range g.members {
			add(m.id)
		}
		return
	}
	add(d.workDepID())
}

// DepIDs returns the step IDs this step depends on.
func (s *WorkStep) DepIDs() []string {
	out := make([]string, len(s.needs))
	copy(out, s.needs)
	return out
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

// Finally marks a step as cleanup. It runs after all declared dependencies
// terminate, even when one failed and fail-fast cancelled ordinary siblings.
// The cleanup uses the Job's parent context rather than the sibling-cancellation
// context; an operator cancellation still stops it through the parent.
func (s *WorkStep) Finally() *WorkStep {
	s.finally = true
	return s
}

// IsFinally reports whether this step is cleanup that survives sibling failure.
func (s *WorkStep) IsFinally() bool { return s.finally }

func (s *WorkStep) markDone(out any) {
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

// Output returns the resolved typed output (after the step completes)
// or nil.
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
// Accepts any [WorkDep], same as [WorkStep.Needs].
func (g *StepGroup) Needs(deps ...WorkDep) *StepGroup {
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

type jobFn struct {
	fn func(ctx context.Context) error
}

func (c *jobFn) Work(w *Work) (*WorkStep, error) {
	Step(w, "run", c.fn)
	return nil, nil
}
