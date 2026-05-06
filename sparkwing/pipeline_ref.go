package sparkwing

import (
	"context"
	"time"
)

// This file holds the cross-pipeline ref RESOLVER infrastructure
// installed on ctx by orchestrator implementations. The user-facing
// Ref[T] type and its RefToLastRun constructor live in ref.go --
// they were merged into the unified Ref[T] in SDK-037 so authors
// have one field type for both in-run and cross-pipeline outputs.

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
