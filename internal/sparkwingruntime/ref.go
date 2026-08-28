package sparkwingruntime

import (
	"context"

	"github.com/sparkwing-dev/sparkwing/sparkwing"
)

func WithJSONResolver(ctx context.Context, get func(nodeID string) ([]byte, bool)) context.Context {
	return context.WithValue(ctx, sparkwing.RuntimePlumbing.Keys.JSONRefResolver, get)
}
