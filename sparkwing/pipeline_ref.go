package sparkwing

import (
	"context"
	"time"
)

// ResolvedPipelineRef is what a PipelineResolver returns: the source
// run id (for audit) + raw output JSON (for Ref[T].Get to unmarshal).
type resolvedPipelineRef struct {
	RunID string
	Data  []byte
}

// PipelineResolver is the backend-facing interface installed on ctx.
// Both the local orchestrator and the cluster pod runner implement it.
// Resolvers also emit an audit trail so the consuming node's event
// stream records which source run fed it.
type pipelineResolver interface {
	resolve(ctx context.Context, pipeline, nodeID string, maxAge time.Duration, fromAnyRun bool) (*resolvedPipelineRef, error)
}

func pipelineResolverFromContext(ctx context.Context) pipelineResolver {
	if r, ok := ctx.Value(keyPipelineResolver).(pipelineResolver); ok {
		return r
	}
	return nil
}

// PipelineResolverFunc adapts a plain function to PipelineResolver.
type pipelineResolverFunc func(ctx context.Context, pipeline, nodeID string, maxAge time.Duration, fromAnyRun bool) (*resolvedPipelineRef, error)

func (f pipelineResolverFunc) resolve(ctx context.Context, pipeline, nodeID string, maxAge time.Duration, fromAnyRun bool) (*resolvedPipelineRef, error) {
	return f(ctx, pipeline, nodeID, maxAge, fromAnyRun)
}
