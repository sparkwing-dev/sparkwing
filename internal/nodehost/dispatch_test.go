package nodehost

import (
	"context"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/capability"
	"github.com/sparkwing-dev/sparkwing/internal/nodeproto"
	"github.com/sparkwing-dev/sparkwing/pkg/match"
	"github.com/sparkwing-dev/sparkwing/pkg/store"
)

func releaseRemote(t *testing.T, f *storeHostFixture, node string) {
	t.Helper()
	f.Host.Local = func(store.NodeRow) bool { return false }
	ref := store.NodeRef{RunID: f.Input.RunID, NodeID: node}
	row, err := f.Tenant.ReadNode(t.Context(), ref)
	if err != nil {
		t.Fatal(err)
	}
	entry, err := f.Host.Acquire(t.Context(), f.Input)
	if err != nil {
		t.Fatal(err)
	}
	if err := f.Host.release(t.Context(), f.Input, f.Accepted, entry, row, store.DecisionReady, func(context.Context, store.ReconcileRequest) error {
		t.Fatal("remote work entered owner executor")
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}

func claimRemote(t *testing.T, f *storeHostFixture, node string) (store.UnitClaim, NodeInput) {
	t.Helper()
	claim, err := f.Store.ClaimUnit(t.Context(), store.UnitDemand{CapabilityPrefix: f.Control.CapabilityPrefix, Target: &store.UnitRef{Kind: capability.UnitNode, RunID: f.Input.RunID, NodeID: node}, Available: match.Resources{Cores: 64, MemoryBytes: 64 << 30}})
	if err != nil {
		t.Fatal(err)
	}
	row, err := f.Tenant.ReadNode(t.Context(), store.NodeRef{RunID: f.Input.RunID, NodeID: node})
	if err != nil {
		t.Fatal(err)
	}
	entry, err := f.Host.Acquire(t.Context(), f.Input)
	if err != nil {
		t.Fatal(err)
	}
	input, err := NewNodeInput(f.Accepted, row, entry.Engine, f.Input.GrantedRunner)
	if err != nil {
		t.Fatal(err)
	}
	return claim, input
}

func remotePlan() nodeproto.PlanDoc {
	return nodeproto.PlanDoc{Jobs: []nodeproto.JobSpec{{ID: "build", Steps: []nodeproto.StepSpec{{ID: "body"}}}, {ID: "test", Needs: []string{"build"}, Steps: []nodeproto.StepSpec{{ID: "body"}}}}}
}

func TestDispatchAdoptsTheRowTheRemoteHostWrote(t *testing.T) {
	f := newStoreHostFixture(t, remotePlan())
	for _, id := range []string{"build", "test"} {
		releaseRemote(t, f, id)
		_, input := claimRemote(t, f, id)
		if err := f.report(t.Context(), input, store.NodeSuccess); err != nil {
			t.Fatal(err)
		}
		before, err := f.Tenant.ReadNode(t.Context(), store.NodeRef{RunID: f.Input.RunID, NodeID: id})
		if err != nil {
			t.Fatal(err)
		}
		result, err := FollowReconciliation(t.Context(), f.State, store.ReconcileRequest{Ref: before.Ref, Actor: f.Actor, Policy: store.SourceNone}, nil)
		if err != nil || result.Kind != store.KeepTerminal || result.Node.Outcome != store.NodeSuccess || result.Node.StateSeq != before.StateSeq || result.Node.Attempt != 1 || result.Node.Generation != input.Generation {
			t.Fatalf("adopted = %+v, error %v", result, err)
		}
	}
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	completion, err := f.Host.Run(ctx, f.Input)
	if err != nil || completion.Outcome != "success" {
		t.Fatalf("completion = %+v, error %v", completion, err)
	}
}

func TestDispatchSendsANodeOnceWhateverItsRetries(t *testing.T) {
	f := newStoreHostFixture(t, nodeproto.PlanDoc{Jobs: []nodeproto.JobSpec{{ID: "flaky", Retry: &nodeproto.RetrySpec{Attempts: 3}, Steps: []nodeproto.StepSpec{{ID: "body"}}}}})
	releaseRemote(t, f, "flaky")
	_, input := claimRemote(t, f, "flaky")
	ref := store.NodeRef{RunID: f.Input.RunID, NodeID: "flaky"}
	held, err := f.Tenant.ReadNode(t.Context(), ref)
	if err != nil {
		t.Fatal(err)
	}
	for range 3 {
		result, err := f.Tenant.ReconcileNode(t.Context(), store.ReconcileRequest{Ref: ref, Actor: f.Actor, Policy: store.SourceNone})
		if err != nil || result.Kind != store.WaitHolder || result.Node.Generation != held.Generation || result.Node.StateSeq != held.StateSeq {
			t.Fatalf("held reconciliation = %+v, error %v", result, err)
		}
	}
	if _, err := f.Store.ClaimUnit(t.Context(), store.UnitDemand{CapabilityPrefix: f.Control.CapabilityPrefix, Target: &store.UnitRef{Kind: capability.UnitNode, RunID: ref.RunID, NodeID: ref.NodeID}, Available: match.Resources{Cores: 64, MemoryBytes: 64 << 30}}); err == nil {
		t.Fatal("held node awarded twice")
	}
	if err := f.report(t.Context(), input, store.NodeFailed); err != nil {
		t.Fatal(err)
	}
	completion, err := f.Host.Run(t.Context(), f.Input)
	if err != nil || completion.Outcome != "failed" {
		t.Fatalf("completion = %+v, error %v", completion, err)
	}
}

func TestDispatchCancelledRunCancelsTheOutstandingNode(t *testing.T) {
	f := newStoreHostFixture(t, remotePlan())
	releaseRemote(t, f, "build")
	_, input := claimRemote(t, f, "build")
	ref := store.NodeRef{RunID: f.Input.RunID, NodeID: "build"}
	if _, err := f.Tenant.TransitionRun(t.Context(), store.RunTransition{RunID: ref.RunID, Actor: f.Control, To: store.RunCancelled, Cause: store.RunCancel}); err != nil {
		t.Fatal(err)
	}
	f.State.notify()
	applied, err := f.State.TransitionNode(t.Context(), store.NodeTransition{Ref: ref, Actor: f.Control, To: store.NodeCancelled, Cause: store.CauseCancel})
	if err != nil || !applied.CancelRequested {
		t.Fatalf("cancel = %+v, error %v", applied, err)
	}
	held, err := f.Tenant.ReadNode(t.Context(), ref)
	if err != nil || held.ClaimedBy != input.Authority.Holder || held.Generation != input.Generation || held.Outcome != "" || held.CancelRequestedAt == nil {
		t.Fatalf("cancelled holder = %+v, error %v", held, err)
	}
	if err := f.report(t.Context(), input, store.NodeCancelled); err != nil {
		t.Fatal(err)
	}
	entry, err := f.Host.Acquire(t.Context(), f.Input)
	if err != nil {
		t.Fatal(err)
	}
	completion, err := f.Host.schedule(t.Context(), f.Input, f.Accepted, entry)
	if err != nil || completion.Outcome != "cancelled" {
		t.Fatalf("completion = %+v, error %v", completion, err)
	}
	dependent, err := f.Tenant.ReadNode(t.Context(), store.NodeRef{RunID: ref.RunID, NodeID: "test"})
	if err != nil || dependent.Outcome != store.NodeCancelled || dependent.Attempt != 0 {
		t.Fatalf("dependent = %+v, error %v", dependent, err)
	}
}
