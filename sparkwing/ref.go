package sparkwing

import (
	"context"
	"encoding/json"
	"fmt"
	"reflect"
)

// Ref is a typed reference to an upstream node's Run output. Declared as
// a field on a downstream job, it is wired at Plan time and resolved at
// dispatch time by the orchestrator.
//
//	type DeployJob struct {
//	    sparkwing.Base
//	    Build sparkwing.Ref[BuildOut]
//	}
//
//	func (j *DeployJob) Run(ctx context.Context) (DeployOut, error) {
//	    build := j.Build.Get(ctx)
//	    return deploy(ctx, build.Tag, build.Digest)
//	}
type Ref[T any] struct {
	// NodeID identifies which node produces the referenced output.
	// Set by sw.Output[T] at plan construction.
	NodeID string
}

// Node returns the upstream node id this reference points at.
func (r Ref[T]) Node() string { return r.NodeID }

// Get resolves the reference via the resolver installed in ctx.
// Panics if no resolver is present (called outside the orchestrator)
// or the upstream hasn't completed.
//
// Two resolver shapes are supported, checked in order:
//
//  1. In-process (single-binary): WithResolver installs a closure
//     returning the typed output directly.
//  2. JSON (cluster-mode pods): WithJSONResolver installs a closure
//     returning raw JSON bytes; Get unmarshals into T at the call site.
func (r Ref[T]) Get(ctx context.Context) T {
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

// Output returns a Ref[T] pointing at a node's Run output. The node's
// job must embed sparkwing.Produces[T]; Output[T] validates against
// it at Plan time so type mismatches panic with a node-id-tagged
// message before any step runs.
//
//	build := sw.Job(plan, "build", &BuildJob{})  // BuildJob embeds sparkwing.Produces[BuildOut]
//	buildOut := sw.Output[BuildOut](build)
//	sw.Job(plan, "deploy", &DeployJob{Build: buildOut}).Needs(build)
//
// Output[T] panics when the job does not embed Produces[T] or T does
// not match the marker's declared type.
func Output[T any](n *Node) Ref[T] {
	var zero T
	want := reflect.TypeOf(zero)
	got := n.OutputType()
	if got == nil {
		panic(fmt.Sprintf(
			"sparkwing: Output[%T]: node %q does not embed sparkwing.Produces[%T] "+
				"(add the marker to the job struct so the contract is visible at the type level)",
			zero, n.id, zero))
	}
	if got != want {
		panic(fmt.Sprintf(
			"sparkwing: Output[%T]: node %q produces %v, not %v",
			zero, n.id, got, want))
	}
	return Ref[T]{NodeID: n.id}
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
