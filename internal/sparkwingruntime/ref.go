package sparkwingruntime

import (
	"context"

	"github.com/sparkwing-dev/sparkwing/sparkwing"
)

func WithJSONResolver(ctx context.Context, get func(nodeID string) ([]byte, bool)) context.Context {
	return context.WithValue(ctx, sparkwing.RuntimePlumbing.Keys.JSONRefResolver, get)
}

// WithOutputReader installs the reader Ref.Get uses for in-run outputs, in
// place of a resolver from WithJSONResolver. An error marked with
// sparkwing.ErrRefAbsent is an output that is absent.
func WithOutputReader(ctx context.Context, read func(ctx context.Context, nodeID string) ([]byte, error)) context.Context {
	return context.WithValue(ctx, sparkwing.RuntimePlumbing.Keys.JSONRefResolver, read)
}
