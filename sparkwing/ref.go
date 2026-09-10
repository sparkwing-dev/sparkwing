package sparkwing

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"time"
)

// Ref is a typed reference to another node's output. Declared as a
// field on a downstream job, it is wired at Plan time and resolved
// at dispatch time via a resolver installed in ctx.
//
// One field type covers two routings:
//
//  1. In-run sibling -- Ref points at a node in the same DAG.
//     Construct via RefTo[T](node). Implies a Needs() edge that
//     the orchestrator picks up.
//
//  2. Cross-pipeline, passive -- Ref points at a node in another
//     pipeline's most recent successful run. Construct via
//     RefToLastRun[T](pipeline, nodeID, opts...). Reading does NOT
//     trigger a run; if you need fresh data, use RunAndAwait
//     imperatively from a step body.
//
// The two routings are distinguished internally by whether
// Pipeline is set; from the call site, .Get(ctx) looks identical:
//
//	type Deploy struct {
//	    sparkwing.Base
//	    Build    sparkwing.Ref[BuildOut]   // in-run
//	    Manifest sparkwing.Ref[Manifest]   // cross-pipeline
//	}
//
//	func (j *Deploy) Run(ctx context.Context) error {
//	    b := j.Build.Get(ctx)
//	    m := j.Manifest.Get(ctx)
//	    return deploy(ctx, b, m)
//	}
type Ref[T any] struct {
	// NodeID identifies the producing node. For in-run refs this is
	// a sibling node id; for cross-pipeline refs this is a node id
	// inside Pipeline.
	NodeID string

	// Pipeline names the upstream pipeline when the ref is
	// cross-pipeline. Empty means in-run.
	Pipeline string

	// MaxAge bounds the freshness of a cross-pipeline run lookup.
	// Zero means "any successful run, however old." Ignored for
	// in-run refs.
	MaxAge time.Duration
}

// Job returns the upstream node id this reference points at.
func (r Ref[T]) Job() string { return r.NodeID }

// Get resolves the reference to a typed T value. Behavior depends
// on the routing:
//
//   - In-run (Pipeline==""): unmarshals the upstream node's stored
//     output into T. Panics if no resolver is installed, or if the
//     upstream hasn't completed.
//
//   - Cross-pipeline (Pipeline!=""): asks the pipeline resolver
//     installed via WithPipelineResolver. Panics if no resolver, if
//     no successful run within MaxAge exists, or if the JSON output
//     fails to unmarshal into T.
//
// Panics rather than returning errors so step bodies stay tight
// for the common case (one-shot read at the top of a step). A
// failed Get is a programmer mistake (missing Needs(), wrong
// pipeline name) and should crash loud. Where a missing output is
// an expected state -- the bootstrap run of a compare-to-last-run
// pipeline has no previous run to read -- use TryGet.
func (r Ref[T]) Get(ctx context.Context) T {
	out, _, err := r.resolve(ctx)
	if err != nil {
		panic(err.Error())
	}
	return out
}

// TryGet resolves the reference like Get but reports an absent upstream
// output instead of panicking. A step can then treat absence as a
// normal state:
//
//	prev, ok := j.Prev.TryGet(ctx)
//	if !ok {
//	    return j.buildEverything(ctx) // bootstrap run: nothing to compare against
//	}
//
// The bootstrap run of a compare-to-last-run pipeline is what this
// exists for: RefToLastRun has no successful run to read on a
// pipeline's first run, and Get panics there.
//
// ok is false when the upstream node has not completed, when the run
// that was found stored no output, and on any cross-pipeline resolver
// failure. The SDK cannot tell an unreachable store from a genuine
// absence -- a resolver returns a bare error -- and reports both as
// absence. For the compare-to-last-run shape this errs toward doing the
// whole job; a step that uses TryGet to SKIP work turns a store outage
// into a silent skip, so read the warn log before relying on that shape.
// Every miss is logged at warn naming the pipeline and node, because
// "no matching run" is also what a misspelled pipeline name and an
// unreachable store produce, and a silent bootstrap branch would hide
// either forever.
//
// One input divides the two accessors: a cross-pipeline run that stored
// empty or null output. Get renders that as the zero T and carries on;
// TryGet reports it as a miss. Swapping one for the other on such a ref
// changes which branch runs.
//
// TryGet panics for the failures a pipeline author cannot handle at
// runtime: no resolver in context, which happens only outside a
// dispatched step; stored output that does not fit T; and a cancelled or
// expired context, which is the step being torn down rather than an
// upstream that is absent. Use Get when a missing output is itself a
// programmer mistake.
func (r Ref[T]) TryGet(ctx context.Context) (T, bool) {
	var zero T
	out, present, err := r.resolve(ctx)
	if err != nil {
		if !errors.As(err, new(*refMiss)) || ctxEnded(err) {
			panic(err.Error())
		}
		Warn(ctx, "Ref.TryGet: %s is absent, treating it as a miss: %v", r.describe(), errors.Unwrap(err))
		return zero, false
	}
	if !present {
		Warn(ctx, "Ref.TryGet: %s stored no output, treating it as a miss", r.describe())
		return zero, false
	}
	return out, true
}

// ctxEnded reports a failure that is the step being torn down rather
// than an upstream output being absent. A resolver flattens every store
// failure into one error, so this is the one case the SDK can still tell
// apart, and treating it as a miss would send a step down its bootstrap
// branch on a dead context.
func ctxEnded(err error) bool {
	return errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded)
}

// refMiss marks a resolution failure TryGet reports rather than panics
// on. The in-run case is an upstream that has not completed; the
// cross-pipeline case is any resolver failure, because a resolver
// returns a bare error the SDK cannot classify.
type refMiss struct{ err error }

func (m *refMiss) Error() string { return m.err.Error() }

// Unwrap keeps the cause reachable, so a caller and a later, narrower
// classification can both still see what the resolver actually returned.
func (m *refMiss) Unwrap() error { return m.err }

func (r Ref[T]) describe() string {
	if r.Pipeline != "" {
		return fmt.Sprintf("%s/%s", r.Pipeline, r.NodeID)
	}
	return r.NodeID
}

// resolve reads the referenced output. present is true only when a
// stored output was read and unmarshalled; Get renders the absent case
// as the zero T and TryGet reports it as a miss.
func (r Ref[T]) resolve(ctx context.Context) (value T, present bool, err error) {
	if r.Pipeline != "" {
		return r.getCrossPipeline(ctx)
	}
	return r.getInRun(ctx)
}

func (r Ref[T]) getInRun(ctx context.Context) (T, bool, error) {
	jsonResolve := jsonResolverFromContext(ctx)
	var out T
	if jsonResolve == nil {
		return out, false, fmt.Errorf("sparkwing: Ref[%T].Get called without a resolver in context", out)
	}
	data, ok := jsonResolve(r.NodeID)
	if !ok {
		return out, false, &refMiss{fmt.Errorf("sparkwing: Ref[%T].Get: node %q has not completed", out, r.NodeID)}
	}
	if err := json.Unmarshal(data, &out); err != nil {
		var zero T
		return zero, false, fmt.Errorf("sparkwing: Ref[%T].Get: unmarshal node %q output: %w", zero, r.NodeID, err)
	}
	Debug(ctx, "Ref.Get(%s): %d bytes", r.NodeID, len(data))
	return out, true, nil
}

func (r Ref[T]) getCrossPipeline(ctx context.Context) (T, bool, error) {
	var out T
	resolver := pipelineResolverFromContext(ctx)
	if resolver == nil {
		return out, false, fmt.Errorf("sparkwing: Ref[%T].Get called without a pipeline resolver in context (pipeline=%q node=%q)",
			out, r.Pipeline, r.NodeID)
	}
	resolved, err := resolver.resolve(ctx, r.Pipeline, r.NodeID, r.MaxAge)
	if err != nil {
		return out, false, &refMiss{fmt.Errorf("sparkwing: Ref[%T].Get failed (pipeline=%q node=%q): %w",
			out, r.Pipeline, r.NodeID, err)}
	}
	if len(resolved.Data) == 0 || string(resolved.Data) == "null" {
		return out, false, nil
	}
	if err := json.Unmarshal(resolved.Data, &out); err != nil {
		var zero T
		return zero, false, fmt.Errorf("sparkwing: Ref[%T].Get: unmarshal %s/%s output from run %s: %w",
			zero, r.Pipeline, r.NodeID, resolved.RunID, err)
	}
	return out, true, nil
}

// RefTo returns a Ref[T] pointing at an in-run node's output. The
// node's job must embed sparkwing.Produces[T]; RefTo validates
// against it at Plan time so type mismatches panic with a
// node-id-tagged message before any step runs.
//
//	build := sw.Job(plan, "build", &Build{}) // Build embeds Produces[BuildOut]
//	buildRef := sw.RefTo[BuildOut](build)
//	sw.Job(plan, "deploy", &Deploy{Build: buildRef}).Needs(build)
//
// RefTo[T] panics when the job does not embed Produces[T] or T does
// not match the marker's declared type.
func RefTo[T any](n *JobNode) Ref[T] {
	var zero T
	want := reflect.TypeOf(zero)
	got := n.OutputType()
	if got == nil {
		panic(fmt.Sprintf(
			"sparkwing: RefTo[%T]: node %q does not embed sparkwing.Produces[%T] "+
				"(add the marker to the job struct so the contract is visible at the type level)",
			zero, n.id, zero,
		))
	}
	if got != want {
		panic(fmt.Sprintf(
			"sparkwing: RefTo[%T]: node %q produces %v, not %v",
			zero, n.id, got, want,
		))
	}
	return Ref[T]{NodeID: n.id}
}

// RefOption tunes a cross-pipeline ref constructor (RefToLastRun).
type RefOption func(*refOpts)

type refOpts struct {
	maxAge time.Duration
}

// MaxAge bounds cross-pipeline ref resolution to runs whose
// finished_at is within d. On miss, Get panics naming the pipeline and
// the age it was given, and TryGet reports the miss, rather than either
// returning stale output.
func MaxAge(d time.Duration) RefOption {
	return func(o *refOpts) { o.maxAge = d }
}

// RefToLastRun returns a Ref[T] pointing at node nodeID in the most
// recent successful run of pipeline. Reading does NOT trigger a
// new run; if you need fresh data tied to the current moment, call
// sparkwing.RunAndAwait imperatively from a step body.
//
// Cross-repo is the primary use case: pipeline A in repo foo can
// consume pipeline B's last output without importing B's Go
// packages. The contract is the wire shape: pipeline name + JSON
// output schema.
//
//	type Deploy struct {
//	    sparkwing.Base
//	    Build sparkwing.Ref[BuildOut]
//	}
//
//	sw.Job(plan, "deploy", &Deploy{
//	    Build: sw.RefToLastRun[BuildOut]("build-pipeline", "artifact",
//	        sw.MaxAge(24*time.Hour),
//	    ),
//	})
//
// A pipeline's first run has no prior successful run to read, so read
// the ref with TryGet unless a missing prior run should crash the step.
func RefToLastRun[T any](pipeline, nodeID string, opts ...RefOption) Ref[T] {
	o := refOpts{}
	for _, opt := range opts {
		opt(&o)
	}
	return Ref[T]{
		NodeID:   nodeID,
		Pipeline: pipeline,
		MaxAge:   o.maxAge,
	}
}

func jsonResolverFromContext(ctx context.Context) func(nodeID string) ([]byte, bool) {
	f, _ := ctx.Value(keyJSONRefResolver).(func(string) ([]byte, bool))
	return f
}

func collectCrossPipelineRefs(job any) []RefTarget {
	t := reflect.TypeOf(job)
	v := reflect.ValueOf(job)
	if t == nil {
		return nil
	}
	for t.Kind() == reflect.Pointer {
		if v.IsNil() {
			return nil
		}
		t = t.Elem()
		v = v.Elem()
	}
	if t.Kind() != reflect.Struct {
		return nil
	}
	var out []RefTarget
	for i := range t.NumField() {
		f := t.Field(i)
		if !f.IsExported() {
			continue
		}
		ft := f.Type
		if ft.Kind() != reflect.Struct {
			continue
		}
		if _, ok := ft.FieldByName("NodeID"); !ok {
			continue
		}
		pf, ok := ft.FieldByName("Pipeline")
		if !ok || pf.Type.Kind() != reflect.String {
			continue
		}
		fv := v.Field(i)
		pipe := fv.FieldByName("Pipeline").String()
		node := fv.FieldByName("NodeID").String()
		if pipe == "" || node == "" {
			continue
		}
		out = append(out, RefTarget{Pipeline: pipe, NodeID: node})
	}
	return out
}

// RefTarget is one (pipeline, node) pair discovered on a job struct
// via collectCrossPipelineRefs. Dashboard / audit code uses it to
// annotate cross-pipeline dependencies.
type RefTarget struct {
	Pipeline string
	NodeID   string
}
