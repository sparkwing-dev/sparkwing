package orchestrator

import (
	"context"
	"fmt"

	"github.com/sparkwing-dev/sparkwing/pkg/store"
	"github.com/sparkwing-dev/sparkwing/sparkwing"
)

type dispatchSpawnHandler struct {
	state        *dispatchState
	parentNodeID string
}

func (h *dispatchSpawnHandler) Spawn(ctx context.Context, parentNodeID, spawnID string, job sparkwing.Workable) (any, error) {
	if h == nil || h.state == nil {
		return nil, fmt.Errorf("orchestrator: spawn handler not bound to a dispatch state")
	}
	if parentNodeID == "" {
		parentNodeID = h.parentNodeID
	}

	child, err := admitSpawnChild(spawnAdmission{
		writeCtx:         h.state.ctx,
		plan:             h.state.plan,
		state:            h.state.backends.State,
		runID:            h.state.runID,
		pipelineRequires: h.state.pipelineRequires,
	}, parentNodeID, spawnID, job)
	if err != nil {
		return nil, err
	}
	childID := child.ID()

	doneCh := h.state.ensureDoneCh(childID)
	h.state.scheduleNode(child)

	resumeProgressTimeout := pauseProgressTimeout(ctx)
	defer resumeProgressTimeout()
	select {
	case <-doneCh:
	case <-ctx.Done():
		return nil, spawnCancelledError(childID, ctx.Err())
	}

	oc, _ := h.state.getOutcome(childID)
	if !oc.OK() {
		return nil, spawnFailedError(childID, oc, h.state.errorMessage(childID))
	}

	out, ok := h.state.resolveJSON(childID)
	if !ok {
		return nil, nil
	}
	return out, nil
}

type spawnAdmission struct {
	writeCtx context.Context

	plan             *sparkwing.Plan
	state            StateBackend
	runID            string
	pipelineRequires []string
}

func admitSpawnChild(a spawnAdmission, parentNodeID, spawnID string, job sparkwing.Workable) (*sparkwing.JobNode, error) {
	if parentNodeID == "" {
		return nil, fmt.Errorf("orchestrator: SpawnNode requires a parent node id (none in ctx)")
	}
	if spawnID == "" {
		return nil, fmt.Errorf("orchestrator: SpawnNode requires a non-empty spawn id")
	}

	childID := parentNodeID + "/" + spawnID
	if a.plan.Job(childID) != nil {
		return nil, fmt.Errorf("orchestrator: SpawnNode id collision: %q already in plan", childID)
	}

	child := sparkwing.NewDetachedNode(childID, job)

	if err := sparkwing.RuntimePlumbing.Fns.PlanInsertChild(a.plan, child); err != nil {
		return nil, fmt.Errorf("orchestrator: insert spawn child %q: %w", childID, err)
	}
	if err := a.state.CreateNode(a.writeCtx, store.Node{
		RunID:       a.runID,
		NodeID:      child.ID(),
		Status:      "pending",
		Deps:        child.DepIDs(),
		NeedsLabels: effectiveClaimLabels(child, a.pipelineRequires),
	}); err != nil {
		return nil, fmt.Errorf("orchestrator: persist spawn child row %q: %w", childID, err)
	}
	_ = a.state.AppendEvent(a.writeCtx, a.runID, parentNodeID, "spawn_dispatched", []byte(childID))
	return child, nil
}

func spawnCancelledError(childID string, err error) error {
	return fmt.Errorf("orchestrator: spawn child %q cancelled before terminal: %w", childID, err)
}

func spawnFailedError(childID string, outcome sparkwing.Outcome, msg string) error {
	if msg == "" {
		msg = string(outcome)
	}
	return fmt.Errorf("spawn child %q failed: %s", childID, msg)
}

func (s *dispatchState) errorMessage(id string) string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.errors[id]
}

func (s *dispatchState) newSpawnHandler(parentNodeID string) sparkwing.SpawnHandler {
	return &dispatchSpawnHandler{state: s, parentNodeID: parentNodeID}
}
