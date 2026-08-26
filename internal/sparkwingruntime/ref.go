package sparkwingruntime

import (
	"context"

	"github.com/sparkwing-dev/sparkwing/sparkwing"
)

// WithJSONResolver installs the in-run reference resolver into ctx. A
// node's output reaches its consumer as JSON in the run store on every
// execution model there is -- a pod, a spawned local process, a node
// executed inside a test binary -- so this is the only resolver.
func WithJSONResolver(ctx context.Context, get func(nodeID string) ([]byte, bool)) context.Context {
	return context.WithValue(ctx, sparkwing.RuntimePlumbing.Keys.JSONRefResolver, get)
}
