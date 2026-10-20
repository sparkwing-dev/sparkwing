package store_test

import (
	"testing"

	"github.com/sparkwing-dev/sparkwing/internal/capability"
	"github.com/sparkwing-dev/sparkwing/internal/controllerwire"
	"github.com/sparkwing-dev/sparkwing/internal/nodeproto"
	"github.com/sparkwing-dev/sparkwing/internal/store"
	"github.com/sparkwing-dev/sparkwing/internal/store/internal/storetest"
)

func checkNodeOrderAfterStarts(t *testing.T, st *store.Store) {
	t.Helper()
	ids := []string{"zulu", "yankee", "xray", "whiskey", "victor"}
	tenant, plan, err := storePlanSealRequestWith(t.Context(), st, func(_ *store.IntakeRequest, doc *nodeproto.PlanDoc) {
		doc.Jobs = nil
		for _, id := range ids {
			doc.Jobs = append(doc.Jobs, nodeproto.JobSpec{ID: id, Steps: []nodeproto.StepSpec{{ID: "body"}}})
		}
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := tenant.AcceptPlan(t.Context(), plan); err != nil {
		t.Fatal(err)
	}
	for i := len(ids) - 1; i >= 0; i-- {
		ref := controllerwire.NodeRef{RunID: plan.RunID, NodeID: ids[i]}
		reconciled, err := tenant.ReconcileNode(t.Context(), store.ReconcileRequest{Ref: ref, Actor: plan.Actor, Policy: store.SourceNone})
		if err != nil || reconciled.Kind != store.Execute || reconciled.Claim == nil {
			t.Fatalf("reconcile %s: %+v, %v", ids[i], reconciled, err)
		}
		claim := reconciled.Claim
		actor := store.Actor{Kind: store.ActorHolder, Authority: capability.AuthorityRef{Kind: capability.AuthorityCapability, ID: claim.CapabilityPrefix}, CapabilityPrefix: claim.CapabilityPrefix, Holder: claim.Holder, Generation: claim.Generation}
		if _, err := tenant.TransitionNode(t.Context(), store.NodeTransition{Ref: ref, Actor: actor, To: store.NodeRunning, Cause: store.CauseStart, SourceAttempt: acceptanceAttempt(0)}); err != nil {
			t.Fatal(err)
		}
	}
	snapshot, err := tenant.ReadSnapshot(t.Context(), plan.RunID)
	nodes := snapshot.Nodes
	if err != nil || len(nodes) != len(ids) {
		t.Fatalf("nodes = %d, %v; want %d", len(nodes), err, len(ids))
	}
	for i, n := range nodes {
		if n.Ref.NodeID != ids[i] || n.Status != store.StatusRunning || n.StartedAt == nil {
			t.Fatalf("node %d = %+v; want running %s", i, n, ids[i])
		}
	}
}

func TestRunSnapshotKeepsDeclarationOrderAcrossStarts(t *testing.T) {
	checkNodeOrderAfterStarts(t, storetest.Open(t, store.WithPolicies(sealStorePolicies)))
}

func TestPostgresRunSnapshotKeepsDeclarationOrderAcrossStarts(t *testing.T) {
	checkNodeOrderAfterStarts(t, openPGTestStore(t))
}
