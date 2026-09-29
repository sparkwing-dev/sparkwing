package orchestrator

import "context"

type (
	processNodeKey  struct{}
	processNode     struct{ runID, nodeID string }
	metricErrorsKey struct{}
)

func withProcessNode(ctx context.Context, runID, nodeID string) context.Context {
	return context.WithValue(ctx, processNodeKey{}, processNode{runID, nodeID})
}

func ownsProcessNode(ctx context.Context, runID, nodeID string) bool {
	owner, ok := ctx.Value(processNodeKey{}).(processNode)
	return ok && owner.runID == runID && owner.nodeID == nodeID
}

func retainMetricError(ctx context.Context, err error) {
	if err == nil {
		return
	}
	if failures, ok := ctx.Value(metricErrorsKey{}).(chan error); ok {
		select {
		case failures <- err:
		default:
		}
	}
}
