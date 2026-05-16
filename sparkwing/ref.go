package sparkwing

import (
	"context"
	"encoding/json"
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
//   - In-run (Pipeline==""): asks the in-run resolver installed via
//     WithResolver / WithJSONResolver. Panics if no resolver, or if
//     the upstream hasn't completed.
//
//   - Cross-pipeline (Pipeline!=""): asks the pipeline resolver
//     installed via WithPipelineResolver. Panics if no resolver, if
//     no successful run within MaxAge exists, or if the JSON output
//     fails to unmarshal into T.
//
// Panics rather than returning errors so step bodies stay tight
// for the common case (one-shot read at the top of a step). A
// failed Get is a programmer mistake (missing Needs(), wrong
// pipeline name) and should crash loud.
func (r Ref[T]) Get(ctx context.Context) T {
	if r.Pipeline != "" {
		return r.getCrossPipeline(ctx)
	}
	return r.getInRun(ctx)
}

func (r Ref[T]) getInRun(ctx context.Context) T {
	if res := resolverFromContext(ctx); res != nil {
		if raw, ok := res.resolve(r.NodeID); ok {
			typed, ok := raw.(T)
			if !ok {
				var zero T
				panic(fmt.Sprintf("sparkwing: Ref[%T].Get: node %q produced %T, not assignable", zero, r.NodeID, raw))
			}
			Debug(ctx, "Ref.Get(%s): in-process hit", r.NodeID)
			return typed
		}
	}
	if jres := jsonResolverFromContext(ctx); jres != nil {
		if data, ok := jres.resolve(r.NodeID); ok {
			var out T
			if err := json.Unmarshal(data, &out); err != nil {
				var zero T
				panic(fmt.Sprintf("sparkwing: Ref[%T].Get: unmarshal node %q output: %v", zero, r.NodeID, err))
			}
			Debug(ctx, "Ref.Get(%s): JSON path, %d bytes", r.NodeID, len(data))
			return out
		}
	}
	var zero T
	if resolverFromContext(ctx) == nil && jsonResolverFromContext(ctx) == nil {
		panic(fmt.Sprintf("sparkwing: Ref[%T].Get called without a resolver in context", zero))
	}
	panic(fmt.Sprintf("sparkwing: Ref[%T].Get: node %q has not completed", zero, r.NodeID))
}

func (r Ref[T]) getCrossPipeline(ctx context.Context) T {
	resolver := pipelineResolverFromContext(ctx)
	if resolver == nil {
		var zero T
		panic(fmt.Sprintf("sparkwing: Ref[%T].Get called without a pipeline resolver in context (pipeline=%q node=%q)",
			zero, r.Pipeline, r.NodeID))
	}
	resolved, err := resolver.resolve(ctx, r.Pipeline, r.NodeID, r.MaxAge)
	if err != nil {
		var zero T
		panic(fmt.Sprintf("sparkwing: Ref[%T].Get failed (pipeline=%q node=%q): %v",
			zero, r.Pipeline, r.NodeID, err))
	}
	var out T
	if len(resolved.Data) == 0 || string(resolved.Data) == "null" {
		return out
	}
	if err := json.Unmarshal(resolved.Data, &out); err != nil {
		var zero T
		panic(fmt.Sprintf("sparkwing: Ref[%T].Get: unmarshal %s/%s output from run %s: %v",
			zero, r.Pipeline, r.NodeID, resolved.RunID, err))
	}
	return out
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
			zero, n.id, zero))
	}
	if got != want {
		panic(fmt.Sprintf(
			"sparkwing: RefTo[%T]: node %q produces %v, not %v",
			zero, n.id, got, want))
	}
	return Ref[T]{NodeID: n.id}
}

// RefOption tunes a cross-pipeline ref constructor (RefToLastRun).
type RefOption func(*refOpts)

type refOpts struct {
	maxAge time.Duration
}

// MaxAge bounds cross-pipeline ref resolution to runs whose
// finished_at is within d. On miss, Get produces a clear "no run
// within X of now" error instead of returning stale output.
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

type refResolver struct {
	get func(nodeID string) (any, bool)
}

func (r *refResolver) resolve(id string) (any, bool) { return r.get(id) }

// WithResolver installs a reference resolver into ctx. Intended for
// orchestrator implementations.
func WithResolver(ctx context.Context, get func(nodeID string) (any, bool)) context.Context {
	return context.WithValue(ctx, keyRefResolver, &refResolver{get: get})
}

func resolverFromContext(ctx context.Context) *refResolver {
	if r, ok := ctx.Value(keyRefResolver).(*refResolver); ok {
		return r
	}
	return nil
}

// jsonRefResolver is the cluster-mode sibling of refResolver: returns
// raw bytes; Ref[T].Get unmarshals. Distinct context key so both
// resolvers can coexist.
type jsonRefResolver struct {
	get func(nodeID string) ([]byte, bool)
}

func (r *jsonRefResolver) resolve(id string) ([]byte, bool) { return r.get(id) }

// WithJSONResolver installs a JSON-returning resolver into ctx. Used
// by cluster-mode pod runners whose only handle to upstream outputs
// is the controller's raw JSON.
func WithJSONResolver(ctx context.Context, get func(nodeID string) ([]byte, bool)) context.Context {
	return context.WithValue(ctx, keyJSONRefResolver, &jsonRefResolver{get: get})
}

func jsonResolverFromContext(ctx context.Context) *jsonRefResolver {
	if r, ok := ctx.Value(keyJSONRefResolver).(*jsonRefResolver); ok {
		return r
	}
	return nil
}

// collectCrossPipelineRefs inspects a job struct for Ref[T] fields
// whose Pipeline is non-empty (i.e. cross-pipeline refs). Used by
// the dashboard / audit code to annotate cross-pipeline dependencies
// without traversing the orchestrator's run graph.
//
// Unlike in-run refs, cross-pipeline refs do NOT imply a Needs()
// edge in the current Plan -- they read another pipeline's history.
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
		// Match Ref[T] structurally: a struct with NodeID + Pipeline
		// string fields. (Generic types don't carry a comparable
		// package-qualified name we can match against directly.)
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
