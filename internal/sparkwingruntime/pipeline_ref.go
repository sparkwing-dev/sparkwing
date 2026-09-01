package sparkwingruntime

import (
	"context"

	"github.com/sparkwing-dev/sparkwing/sparkwing"
)

func WithPipelineResolver(ctx context.Context, r sparkwing.PipelineResolver) context.Context {
	return context.WithValue(ctx, sparkwing.RuntimePlumbing.Keys.PipelineResolver, r)
}
