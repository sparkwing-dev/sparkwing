package sparkwingruntime

import (
	"context"

	"github.com/sparkwing-dev/sparkwing/sparkwing"
)

func WithProfileResolution(ctx context.Context, pr sparkwing.ProfileResolutionContext) context.Context {
	if pr.Name == "" && !pr.IsLocal {
		return ctx
	}
	return context.WithValue(ctx, sparkwing.RuntimePlumbing.Keys.ProfileResolution, pr)
}
