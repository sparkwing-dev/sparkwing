package sparkwingruntime

import (
	"context"

	"github.com/sparkwing-dev/sparkwing/sparkwing"
)

// WithResolver installs a reference resolver into ctx. Intended for
// orchestrator implementations.
func WithResolver(ctx context.Context, get func(nodeID string) (any, bool)) context.Context {
	return context.WithValue(ctx, sparkwing.RuntimePlumbing.Keys.RefResolver, get)
}

// WithJSONResolver installs a JSON-returning resolver into ctx. Used
// by cluster-mode pod runners whose only handle to upstream outputs
// is the controller's raw JSON.
func WithJSONResolver(ctx context.Context, get func(nodeID string) ([]byte, bool)) context.Context {
	return context.WithValue(ctx, sparkwing.RuntimePlumbing.Keys.JSONRefResolver, get)
}
