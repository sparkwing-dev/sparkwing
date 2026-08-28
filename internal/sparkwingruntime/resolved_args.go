package sparkwingruntime

import (
	"context"

	"github.com/sparkwing-dev/sparkwing/sparkwing"
)

func WithResolvedArgs(ctx context.Context, args map[string]any) context.Context {
	if args == nil {
		return ctx
	}
	return context.WithValue(ctx, sparkwing.RuntimePlumbing.Keys.ResolvedArgs, args)
}
