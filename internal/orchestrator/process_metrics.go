package orchestrator

import (
	"context"
)

type (
	processNodeKey struct{}
	processNode    struct{ runID, nodeID string }
)

func withProcessNode(ctx context.Context, runID, nodeID string) context.Context {
	return context.WithValue(ctx, processNodeKey{}, processNode{runID, nodeID})
}
