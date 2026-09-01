package sparkwing

import "context"

// SpawnHandler is the orchestrator-provided callback that fires a
// SpawnNode declaration from inside an executing Work. RunWork calls
// this when a step DAG hits a spawn whose deps are satisfied; the
// handler creates the namespaced Plan node, dispatches it through the
// orchestrator's normal scheduling loop, and blocks until the child
// reaches terminal.
//
// The handler returns the child's typed output (or nil) on success,
// or the failure error.
type SpawnHandler interface {
	Spawn(ctx context.Context, parentNodeID, spawnID string, job Workable) (output any, err error)
}

// SpawnHandlerFunc adapts a closure into a SpawnHandler.
type SpawnHandlerFunc func(ctx context.Context, parentNodeID, spawnID string, job Workable) (any, error)

// Spawn implements SpawnHandler.
func (f SpawnHandlerFunc) Spawn(ctx context.Context, parentNodeID, spawnID string, job Workable) (any, error) {
	return f(ctx, parentNodeID, spawnID, job)
}

func spawnHandlerFromContext(ctx context.Context) SpawnHandler {
	if h, ok := ctx.Value(keySpawnHandler).(SpawnHandler); ok {
		return h
	}
	return nil
}
