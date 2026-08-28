package sparkwingruntime

import (
	"context"

	"github.com/sparkwing-dev/sparkwing/sparkwing"
)

func WithSpawnHandler(ctx context.Context, h sparkwing.SpawnHandler) context.Context {
	return context.WithValue(ctx, sparkwing.RuntimePlumbing.Keys.SpawnHandler, h)
}

func SpawnHandlerFromContext(ctx context.Context) sparkwing.SpawnHandler {
	if h, ok := ctx.Value(sparkwing.RuntimePlumbing.Keys.SpawnHandler).(sparkwing.SpawnHandler); ok {
		return h
	}
	return nil
}
