package orchestrator_test

import (
	"context"
	"testing"

	"github.com/sparkwing-dev/sparkwing/internal/api"
	"github.com/sparkwing-dev/sparkwing/orchestrator"
	"github.com/sparkwing-dev/sparkwing/orchestrator/store"
)

// TestApproval_DecorationCarriesResolution drives a tiny approval
// pipeline (timeout=200ms, policy=fail by default) through the same
// store -> ListApprovalsForRun -> DecorateNodes -> NodeApprovalState
// path the dashboard reads from /api/v1/runs/{id}?include=nodes.
// The decorated gate node should expose who resolved it and when.
func TestApproval_DecorationCarriesResolution(t *testing.T) {
	p := newPaths(t)
	res, err := orchestrator.RunLocal(context.Background(), p,
		orchestrator.Options{Pipeline: "appr-timeout"})
	if err != nil {
		t.Fatalf("RunLocal: %v", err)
	}
	if res.Status != "failed" {
		// approve-timeout's default policy is fail; the run failing
		// is the expected outcome and means the gate did resolve.
		t.Fatalf("status = %q, want failed (default timeout policy)", res.Status)
	}

	st, err := store.Open(p.StateDB())
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	defer st.Close()

	run, err := st.GetRun(context.Background(), res.RunID)
	if err != nil {
		t.Fatalf("GetRun: %v", err)
	}
	nodes, err := st.ListNodes(context.Background(), res.RunID)
	if err != nil {
		t.Fatalf("ListNodes: %v", err)
	}
	approvals, err := st.ListApprovalsForRun(context.Background(), res.RunID)
	if err != nil {
		t.Fatalf("ListApprovalsForRun: %v", err)
	}
	if len(approvals) != 1 {
		t.Fatalf("approvals=%d, want 1", len(approvals))
	}

	decorated := api.DecorateNodes(nodes, run.PlanSnapshot, nil, approvals, nil)
	var gate *api.NodeWithDecorations
	for _, n := range decorated {
		if n.NodeID == "gate" {
			gate = n
			break
		}
	}
	if gate == nil {
		t.Fatalf("gate node missing")
	}
	if gate.Decorations == nil || gate.Decorations.ApprovalState == nil {
		t.Fatalf("ApprovalState missing: %+v", gate.Decorations)
	}
	state := gate.Decorations.ApprovalState
	if state.Resolution != store.ApprovalResolutionTimedOut {
		t.Errorf("Resolution = %q, want %q", state.Resolution, store.ApprovalResolutionTimedOut)
	}
	if state.Approver != "sparkwing" {
		t.Errorf("Approver = %q, want %q (orchestrator-written timeout)", state.Approver, "sparkwing")
	}
	if state.ResolvedAt == nil {
		t.Error("ResolvedAt should be set on a resolved gate")
	}
	if state.Message != "fast timeout" {
		t.Errorf("Message = %q, want %q", state.Message, "fast timeout")
	}
}
