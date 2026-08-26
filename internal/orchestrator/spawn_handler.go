package orchestrator

import (
	"context"
	"fmt"

	"github.com/sparkwing-dev/sparkwing/pkg/store"
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

// spawnAdmission is what a SpawnNode call needs before the child's
// body can run, whichever process ends up running it.
type spawnAdmission struct {
	// writeCtx outlives the spawning node: the child's row and the
	// parent's spawn event are run-level facts, so a parent cancelled
	// mid-spawn must not leave a child that ran with no row naming it.
	writeCtx context.Context
	// plan is the spawning process's own plan object. It carries the
	// children spawned so far, which is what makes a repeated spawn id a
	// collision rather than a second row.
	plan             *sparkwing.Plan
	state            StateBackend
	runID            string
	pipelineRequires []string
}

// admitSpawnChild names a SpawnNode child, splices it into the
// spawning process's plan, and persists its row and the parent's
// spawn_dispatched event.
//
// Both spawn paths go through it -- the dispatcher's, which then
// schedules the child as a plan node, and a node process's, which runs
// the child itself as its own dynamic sub-work. That shared step is
// what makes the run record identical either way: same "<parent>/<id>"
// naming, same collision rule, same pending row with the same claim
// labels, same event on the parent.
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

// spawnCancelledError is what a spawning step sees when its own
// context ends before the child reached a terminal outcome.
func spawnCancelledError(childID string, err error) error {
	return fmt.Errorf("orchestrator: spawn child %q cancelled before terminal: %w", childID, err)
}

// spawnFailedError renders a non-OK child outcome for the spawning
// step. msg is the child's own failure text; the outcome stands in
// when there is none, so a cancelled or skipped child still says which.
func spawnFailedError(childID string, outcome sparkwing.Outcome, msg string) error {
	if msg == "" {
		msg = string(outcome)
	}
	return fmt.Errorf("spawn child %q failed: %s", childID, msg)
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
