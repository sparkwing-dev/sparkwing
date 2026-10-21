package nodehost

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/runspec"

	"github.com/sparkwing-dev/sparkwing/internal/controllerwire"
	"github.com/sparkwing-dev/sparkwing/internal/nodeproto"
	"github.com/sparkwing-dev/sparkwing/internal/store"
	"github.com/sparkwing-dev/sparkwing/internal/store/teststore"
)

func approvalFixture(t *testing.T, gate nodeproto.ApprovalSpec) *storeHostFixture {
	t.Helper()
	f := newStoreHostFixture(t, nodeproto.PlanDoc{Jobs: []nodeproto.JobSpec{
		{ID: "gate", Approval: &gate},
		{ID: "deploy", Needs: []string{"gate"}, Steps: []nodeproto.StepSpec{{ID: "body"}}},
		{ID: "rollback", OnFailureOf: "gate", Steps: []nodeproto.StepSpec{{ID: "body"}}},
	}})
	row, err := f.Tenant.ReadNode(t.Context(), controllerwire.NodeRef{RunID: runspec.RunID(f.Input.RunID), NodeID: "gate"})
	if err != nil {
		t.Fatal(err)
	}
	entry, err := f.Host.Acquire(t.Context(), f.Input)
	if err != nil {
		t.Fatal(err)
	}
	err = f.Host.release(t.Context(), f.Input, f.Accepted, entry, row, store.DecisionApproval, func(context.Context, store.ReconcileRequest) error {
		t.Fatal("approval entered the attempt executor")
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return f
}

func TestApprovalGateResolvesAsTheDispatcherDoes(t *testing.T) {
	for _, tc := range []struct {
		name, resolution, policy string
		outcome                  store.NodeState
	}{
		{"granted", "approved", "", store.NodeSuccess},
		{"denied", "denied", "", store.NodeFailed},
		{"timed out failing", "timed_out", "fail", store.NodeFailed},
		{"timed out denying", "timed_out", "deny", store.NodeFailed},
		{"timed out approving", "timed_out", "approve", store.NodeSuccess},
	} {
		t.Run(tc.name, func(t *testing.T) {
			timeout := int64(1000)
			f := approvalFixture(t, nodeproto.ApprovalSpec{Message: "ship it?", TimeoutMS: &timeout, OnTimeout: tc.policy})
			ref := controllerwire.NodeRef{RunID: runspec.RunID(f.Input.RunID), NodeID: "gate"}
			var change store.NodeCommand = store.ResolveApprovalNode{Ref: ref, Actor: f.Control, Outcome: tc.outcome, Approval: &store.ApprovalResolution{Decision: tc.resolution, Comment: "looks good"}}
			if tc.resolution == "timed_out" {
				if _, err := teststore.SQL(t, f.Store).ExecContext(t.Context(), "UPDATE approvals SET requested_at=? WHERE team=? AND run_id=? AND node_id=?", time.Now().Add(-time.Hour).UnixNano(), store.DefaultTeam, ref.RunID, ref.NodeID); err != nil {
					t.Fatal(err)
				}
				change = store.TimeoutApprovalNode{Ref: ref, Actor: f.Actor}
			}
			if _, err := f.State.TransitionNode(t.Context(), change); err != nil {
				t.Fatal(err)
			}
			row, err := f.Tenant.ReadNode(t.Context(), ref)
			if err != nil || row.Status != store.StatusDone || row.Outcome != tc.outcome || row.Attempt != 0 || row.Generation != 0 {
				t.Fatalf("gate = %+v, error %v", row, err)
			}
			approval, err := f.Tenant.GetApproval(t.Context(), string(ref.RunID), string(ref.NodeID))
			if err != nil || approval.Resolution != tc.resolution || approval.Message != "ship it?" || approval.OnTimeout == "" || approval.ResolvedAt == nil {
				t.Fatalf("approval = %+v, error %v", approval, err)
			}
			snapshot, err := f.Tenant.ReadSnapshot(t.Context(), string(ref.RunID))
			if err != nil {
				t.Fatal(err)
			}
			graph, err := snapshot.Scheduling()
			if err != nil {
				t.Fatal(err)
			}
			decision := store.Decide(graph)
			ready := "deploy"
			if tc.outcome == store.NodeFailed {
				ready = "rollback"
			}
			found := false
			for _, node := range decision.Nodes {
				if node.NodeID == ready && node.Kind == store.DecisionReady {
					found = true
				}
			}
			if !found {
				t.Fatalf("decision = %+v, want %s ready", decision, ready)
			}
		})
	}
}

func TestApprovalGateCancelledWithTheRun(t *testing.T) {
	f := approvalFixture(t, nodeproto.ApprovalSpec{})
	ref := controllerwire.NodeRef{RunID: runspec.RunID(f.Input.RunID), NodeID: "gate"}
	if _, err := f.State.TransitionNode(t.Context(), store.CancelNode{Ref: ref, Actor: f.Control}); err != nil {
		t.Fatal(err)
	}
	row, err := f.Tenant.ReadNode(t.Context(), ref)
	if err != nil || row.Outcome != store.NodeCancelled || row.Attempt != 0 {
		t.Fatalf("gate = %+v, error %v", row, err)
	}
	approval, err := f.Tenant.GetApproval(t.Context(), string(ref.RunID), string(ref.NodeID))
	if err != nil || approval.ResolvedAt != nil {
		t.Fatalf("approval = %+v, error %v", approval, err)
	}
}

func TestApprovalGateTimeoutYieldsToALateHumanAnswer(t *testing.T) {
	timeout := int64(1000)
	f := approvalFixture(t, nodeproto.ApprovalSpec{TimeoutMS: &timeout})
	ref := controllerwire.NodeRef{RunID: runspec.RunID(f.Input.RunID), NodeID: "gate"}
	if _, err := teststore.SQL(t, f.Store).ExecContext(t.Context(), "UPDATE approvals SET requested_at=? WHERE team=? AND run_id=? AND node_id=?", time.Now().Add(-time.Hour).UnixNano(), store.DefaultTeam, ref.RunID, ref.NodeID); err != nil {
		t.Fatal(err)
	}
	if _, err := f.State.TransitionNode(t.Context(), store.ResolveApprovalNode{Ref: ref, Actor: f.Control, Outcome: store.NodeSuccess, Approval: &store.ApprovalResolution{Decision: "approved"}}); err != nil {
		t.Fatal(err)
	}
	_, err := f.State.TransitionNode(t.Context(), store.TimeoutApprovalNode{Ref: ref, Actor: f.Actor})
	var refused *store.ErrIllegalTransition
	if !errors.As(err, &refused) {
		t.Fatalf("late timeout = %v, want terminal refusal", err)
	}
	row, err := f.Tenant.ReadNode(t.Context(), ref)
	if err != nil || row.Outcome != store.NodeSuccess {
		t.Fatalf("gate = %+v, error %v", row, err)
	}
}

func TestApprovalGateFailsOnAStoreThatCannotRecover(t *testing.T) {
	f := newStoreHostFixture(t, nodeproto.PlanDoc{Jobs: []nodeproto.JobSpec{{ID: "gate", Approval: &nodeproto.ApprovalSpec{}}}})
	if _, err := teststore.SQL(t, f.Store).ExecContext(t.Context(), `CREATE TRIGGER refuse_approval BEFORE INSERT ON approvals BEGIN SELECT RAISE(ABORT,'locking protocol'); END`); err != nil {
		t.Fatal(err)
	}
	ref := controllerwire.NodeRef{RunID: runspec.RunID(f.Input.RunID), NodeID: "gate"}
	row, err := f.Tenant.ReadNode(t.Context(), ref)
	if err != nil {
		t.Fatal(err)
	}
	entry, err := f.Host.Acquire(t.Context(), f.Input)
	if err != nil {
		t.Fatal(err)
	}
	err = f.Host.release(t.Context(), f.Input, f.Accepted, entry, row, store.DecisionApproval, func(context.Context, store.ReconcileRequest) error {
		t.Fatal("failed approval entered executor")
		return nil
	})
	if err == nil || !strings.Contains(err.Error(), "locking protocol") {
		t.Fatalf("release = %v, want persisted refusal", err)
	}
	after, err := f.Tenant.ReadNode(t.Context(), ref)
	if err != nil || after.Status != row.Status || after.StateSeq != row.StateSeq {
		t.Fatalf("partial transition = %+v, error %v", after, err)
	}
	if _, err := f.Tenant.GetApproval(t.Context(), string(ref.RunID), string(ref.NodeID)); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("partial approval = %v", err)
	}
}
