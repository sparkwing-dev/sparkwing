package orchestrator

import (
	"context"
	"fmt"

	"github.com/sparkwing-dev/sparkwing/orchestrator/store"
	"github.com/sparkwing-dev/sparkwing/sparkwing"
)

// dispatchSpawnHandler binds a SpawnNode call site to the active
// dispatchState. One per node execution; pins the parent id.
type dispatchSpawnHandler struct {
	state        *dispatchState
	parentNodeID string
}

// Spawn creates a child node, splices it into the plan, persists,
// dispatches, and blocks until terminal.
func (h *dispatchSpawnHandler) Spawn(ctx context.Context, parentNodeID, spawnID string, job sparkwing.Workable) (any, error) {
	if h == nil || h.state == nil {
		return nil, fmt.Errorf("orchestrator: spawn handler not bound to a dispatch state")
	}
	if parentNodeID == "" {
		parentNodeID = h.parentNodeID
	}
	if parentNodeID == "" {
		return nil, fmt.Errorf("orchestrator: SpawnNode requires a parent node id (none in ctx)")
	}
	if spawnID == "" {
		return nil, fmt.Errorf("orchestrator: SpawnNode requires a non-empty spawn id")
	}

	childID := parentNodeID + "/" + spawnID
	if h.state.plan.Node(childID) != nil {
		return nil, fmt.Errorf("orchestrator: SpawnNode id collision: %q already in plan", childID)
	}

	child := sparkwing.JobNode(childID, job)

	if err := h.state.plan.InsertChild(child); err != nil {
		return nil, fmt.Errorf("orchestrator: insert spawn child %q: %w", childID, err)
	}
	if err := h.state.backends.State.CreateNode(h.state.ctx, store.Node{
		RunID:       h.state.runID,
		NodeID:      child.ID(),
		Status:      "pending",
		Deps:        child.DepIDs(),
		NeedsLabels: child.RunsOnLabels(),
	}); err != nil {
		return nil, fmt.Errorf("orchestrator: persist spawn child row %q: %w", childID, err)
	}
	_ = h.state.backends.State.AppendEvent(h.state.ctx, h.state.runID, parentNodeID,
		"spawn_dispatched", []byte(childID))

	doneCh := h.state.ensureDoneCh(child.ID())
	h.state.scheduleNode(child)

	select {
	case <-doneCh:
	case <-ctx.Done():
		return nil, fmt.Errorf("orchestrator: spawn child %q cancelled before terminal: %w", childID, ctx.Err())
	}

	oc, _ := h.state.getOutcome(child.ID())
	if !oc.OK() {
		msg := h.state.errorMessage(child.ID())
		if msg == "" {
			msg = string(oc)
		}
		return nil, fmt.Errorf("spawn child %q failed: %s", childID, msg)
	}

	out, _ := h.state.resolve(child.ID())
	return out, nil
}

// errorMessage returns the per-node error message, or "" if none.
func (s *dispatchState) errorMessage(id string) string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.errors[id]
}

// newSpawnHandler returns a SpawnHandler bound to s.
func (s *dispatchState) newSpawnHandler(parentNodeID string) sparkwing.SpawnHandler {
	return &dispatchSpawnHandler{state: s, parentNodeID: parentNodeID}
}
