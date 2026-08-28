package sparkwingruntime

import (
	"context"

	"github.com/sparkwing-dev/sparkwing/sparkwing"
)

func WithDryRun(ctx context.Context) context.Context {
	return context.WithValue(ctx, sparkwing.RuntimePlumbing.Keys.DryRun, true)
}
