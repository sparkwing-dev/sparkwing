package sparkwing

import (
	"context"
	"encoding/json"
	"fmt"
	"reflect"
	"time"
)

// PipelineRef is a typed reference to a node's output in another
// pipeline's latest successful run.
//
// Use this when pipeline A needs to consume pipeline B's output
// without re-running B. The reference is resolved at dispatch time --
// it reads whatever B last produced, not a snapshot from plan-build
// time. See MaxAge for the freshness bound.
//
//	type DeployJob struct {
//	    sparkwing.Base
//	    Build sparkwing.PipelineRef[BuildOut, BuildInputs]
//	}
//
//	p.Add("deploy", &DeployJob{
//	    Build: sparkwing.FromPipeline[BuildOut, BuildInputs](
//	        "build-pipeline", "artifact",
//	        sparkwing.MaxAge(24*time.Hour),
//	    ),
//	})
//
// Cross-repo is the primary use case: A and B don't have to share a
// Go binary. The second type parameter In names the target pipeline's
// Inputs struct; it carries call-site discipline only (Get reads the
// most recent successful run rather than spawning a new one).
type PipelineRef[Out, In any] struct {
	// Pipeline is the name of the upstream pipeline as registered via
	// sparkwing.Register.
	Pipeline string

	// NodeID identifies which node in the upstream pipeline produces
	// the output this ref consumes.
	NodeID string

	// MaxAge bounds the freshness of the resolved run. When > 0, Get
	// fails if no successful run within MaxAge exists. Zero means
	// "any successful run, however old."
	MaxAge time.Duration
}

// RefOption is a constructor option for FromPipeline.
type RefOption func(*pipelineRefOpts)

type pipelineRefOpts struct {
	maxAge time.Duration
}

// MaxAge bounds PipelineRef resolution to runs whose finished_at is
// within d. On miss, Get produces a clear "no run within X of now"
// error instead of returning stale output.
func MaxAge(d time.Duration) RefOption {
	return func(o *pipelineRefOpts) { o.maxAge = d }
}

// FromPipeline constructs a PipelineRef[Out, In] pointing at node
// nodeID in the latest successful run of pipeline.
func FromPipeline[Out, In any](pipeline, nodeID string, opts ...RefOption) PipelineRef[Out, In] {
	o := pipelineRefOpts{}
	for _, opt := range opts {
		opt(&o)
	}
	return PipelineRef[Out, In]{
		Pipeline: pipeline,
		NodeID:   nodeID,
		MaxAge:   o.maxAge,
	}
}

// Get resolves the reference to the typed output of the target node
// in the target pipeline's latest matching run. Panics if no pipeline
// resolver is installed in ctx, if no matching run exists, or if the
// JSON output fails to unmarshal into Out.
func (r PipelineRef[Out, In]) Get(ctx context.Context) Out {
	resolver := pipelineResolverFromContext(ctx)
	if resolver == nil {
		var zero Out
		panic(fmt.Sprintf("sparkwing: PipelineRef[%T].Get called without a pipeline resolver in context (pipeline=%q node=%q)",
			zero, r.Pipeline, r.NodeID))
	}
	resolved, err := resolver.resolve(ctx, r.Pipeline, r.NodeID, r.MaxAge)
	if err != nil {
		var zero Out
		panic(fmt.Sprintf("sparkwing: PipelineRef[%T].Get failed (pipeline=%q node=%q): %v",
			zero, r.Pipeline, r.NodeID, err))
	}
	var out Out
	if len(resolved.Data) == 0 || string(resolved.Data) == "null" {
		return out
	}
	if err := json.Unmarshal(resolved.Data, &out); err != nil {
		var zero Out
		panic(fmt.Sprintf("sparkwing: PipelineRef[%T].Get: unmarshal %s/%s output from run %s: %v",
			zero, r.Pipeline, r.NodeID, resolved.RunID, err))
	}
	return out
}

// ResolvedPipelineRef is what a PipelineResolver returns: the source
// run id (for audit) + raw output JSON (for Ref[T].Get to unmarshal).
type ResolvedPipelineRef struct {
	RunID string
	Data  []byte
}

// PipelineResolver is the backend-facing interface installed on ctx.
// Both the local orchestrator and the cluster pod runner implement it.
// Resolvers also emit an audit trail so the consuming node's event
// stream records which source run fed it.
type PipelineResolver interface {
	resolve(ctx context.Context, pipeline, nodeID string, maxAge time.Duration) (*ResolvedPipelineRef, error)
}

// WithPipelineResolver installs a PipelineResolver into ctx. Intended
// for orchestrator implementations.
func WithPipelineResolver(ctx context.Context, r PipelineResolver) context.Context {
	return context.WithValue(ctx, keyPipelineResolver, r)
}

func pipelineResolverFromContext(ctx context.Context) PipelineResolver {
	if r, ok := ctx.Value(keyPipelineResolver).(PipelineResolver); ok {
		return r
	}
	return nil
}

// PipelineResolverFunc adapts a plain function to PipelineResolver.
type PipelineResolverFunc func(ctx context.Context, pipeline, nodeID string, maxAge time.Duration) (*ResolvedPipelineRef, error)

func (f PipelineResolverFunc) resolve(ctx context.Context, pipeline, nodeID string, maxAge time.Duration) (*ResolvedPipelineRef, error) {
	return f(ctx, pipeline, nodeID, maxAge)
}

// collectPipelineRefs inspects a job struct for PipelineRef[T] fields.
// Unlike ordinary Ref, PipelineRef does NOT imply an in-plan
// dependency edge.
func collectPipelineRefs(job any) []PipelineRefPair {
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
	var out []PipelineRefPair
	for i := range t.NumField() {
		f := t.Field(i)
		if !f.IsExported() {
			continue
		}
		// Structural match: a struct with fields Pipeline (string)
		// and NodeID (string). Generic types don't carry a
		// package-qualified name we can compare against.
		ft := f.Type
		if ft.Kind() != reflect.Struct {
			continue
		}
		pf, pok := ft.FieldByName("Pipeline")
		nf, nok := ft.FieldByName("NodeID")
		if !pok || !nok {
			continue
		}
		if pf.Type.Kind() != reflect.String || nf.Type.Kind() != reflect.String {
			continue
		}
		fv := v.Field(i)
		pipe := fv.FieldByName("Pipeline").String()
		node := fv.FieldByName("NodeID").String()
		if pipe == "" || node == "" {
			continue
		}
		out = append(out, PipelineRefPair{Pipeline: pipe, NodeID: node})
	}
	return out
}

// PipelineRefPair is one (pipeline, node) pair discovered on a job
// struct. Dashboard / audit code uses it to annotate cross-pipeline
// dependencies.
type PipelineRefPair struct {
	Pipeline string
	NodeID   string
}
