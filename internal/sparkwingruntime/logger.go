package sparkwingruntime

import (
	"context"

	"github.com/sparkwing-dev/sparkwing/sparkwing"
)

func WithLogger(ctx context.Context, l sparkwing.Logger) context.Context {
	return context.WithValue(ctx, sparkwing.RuntimePlumbing.Keys.Logger, l)
}

func WithNode(ctx context.Context, nodeID string) context.Context {
	return context.WithValue(ctx, sparkwing.RuntimePlumbing.Keys.Node, nodeID)
}
