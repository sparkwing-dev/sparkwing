package sparkwingruntime

import (
	"context"

	"github.com/sparkwing-dev/sparkwing/sparkwing"
)

// WithLogger returns a derived context carrying the given logger.
func WithLogger(ctx context.Context, l sparkwing.Logger) context.Context {
	return context.WithValue(ctx, sparkwing.RuntimePlumbing.Keys.Logger, l)
}

// WithNode installs the current node ID into ctx. Exec primitives
// tag their emitted lines with this ID so logs are attributable.
func WithNode(ctx context.Context, nodeID string) context.Context {
	return context.WithValue(ctx, sparkwing.RuntimePlumbing.Keys.Node, nodeID)
}
