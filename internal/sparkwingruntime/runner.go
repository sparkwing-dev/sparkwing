package sparkwingruntime

import (
	"context"

	"github.com/sparkwing-dev/sparkwing/sparkwing"
)

func WithRunner(ctx context.Context, r *sparkwing.RunnerInfo) context.Context {
	if ctx == nil {
		ctx = context.Background()
	}
	return context.WithValue(ctx, sparkwing.RuntimePlumbing.Keys.Runner, r)
}
