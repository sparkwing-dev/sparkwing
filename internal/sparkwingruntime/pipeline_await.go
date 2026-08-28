package sparkwingruntime

import (
	"context"

	"github.com/sparkwing-dev/sparkwing/sparkwing"
)

func WithPipelineAwaiter(ctx context.Context, a sparkwing.PipelineAwaiter) context.Context {
	return context.WithValue(ctx, sparkwing.RuntimePlumbing.Keys.PipelineAwaiter, a)
}
