package sparkwingruntime

import (
	"context"

	"github.com/sparkwing-dev/sparkwing/sparkwing"
)

// WithPipelineAwaiter installs a PipelineAwaiter into ctx. Intended
// for orchestrator implementations.
func WithPipelineAwaiter(ctx context.Context, a sparkwing.PipelineAwaiter) context.Context {
	return context.WithValue(ctx, sparkwing.RuntimePlumbing.Keys.PipelineAwaiter, a)
}
