package store_test

import (
	"testing"

	"github.com/sparkwing-dev/sparkwing/internal/capability"
	"github.com/sparkwing-dev/sparkwing/internal/nodeproto"
	"github.com/sparkwing-dev/sparkwing/internal/runspec"
	"github.com/sparkwing-dev/sparkwing/internal/runstate"
	"github.com/sparkwing-dev/sparkwing/internal/store"
	"github.com/sparkwing-dev/sparkwing/internal/store/internal/storetest"
)

func checkNodeOrderAfterStarts(t *testing.T, target *storetest.Target) {
	t.Helper()
	st := target.Open(t, store.WithPolicies(sealStorePolicies))
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
		ref := runspec.NodeRef{RunID: runspec.RunID(plan.RunID), NodeID: runspec.NodeID(ids[i])}
		reconciled, err := tenant.ReconcileNode(t.Context(), store.ReconcileRequest{Ref: ref, Actor: plan.Actor, Policy: runstate.SourceNone})
		if err != nil || reconciled.Kind != runstate.Execute || reconciled.Claim == nil {
			t.Fatalf("reconcile %s: %+v, %v", ids[i], reconciled, err)
		}
		claim := reconciled.Claim
		actor := runstate.Actor{Kind: runstate.ActorHolder, Authority: capability.AuthorityRef{Kind: capability.AuthorityCapability, ID: claim.CapabilityPrefix}, CapabilityPrefix: claim.CapabilityPrefix, NodeFence: runspec.NodeFence{RunID: ref.RunID, NodeID: ref.NodeID, Holder: claim.Holder, Generation: claim.Generation}}
		if result, err := tenant.MayStart(t.Context(), runstate.CapacityBinding{RunID: string(actor.RunID), NodeID: string(actor.NodeID), Generation: int64(actor.Generation)}, actor); err != nil || result.Kind != runstate.AcquireGranted {
			t.Fatalf("execution admission: %+v, %v", result, err)
		}
		if _, err := tenant.TransitionNode(t.Context(), store.StartNode{Ref: ref, Actor: actor, SourceAttempt: acceptanceAttempt(0)}); err != nil {
			t.Fatal(err)
		}
	}
	snapshot, err := tenant.ReadSnapshot(t.Context(), plan.RunID)
	nodes := snapshot.Nodes
	if err != nil || len(nodes) != len(ids) {
		t.Fatalf("nodes = %d, %v; want %d", len(nodes), err, len(ids))
	}
	for i, n := range nodes {
		if n.Ref.NodeID != runspec.NodeID(ids[i]) || n.Status != runstate.StatusRunning || n.StartedAt == nil {
			t.Fatalf("node %d = %+v; want running %s", i, n, ids[i])
		}
	}
}

func TestRunSnapshotKeepsDeclarationOrderAcrossStarts(t *testing.T) {
	checkNodeOrderAfterStarts(t, storetest.NewSQLite(t))
}

func TestPostgresRunSnapshotKeepsDeclarationOrderAcrossStarts(t *testing.T) {
	checkNodeOrderAfterStarts(t, storetest.NewPostgres(t))
}

func TestPlanFixtureReconcileUsesConfiguredClaimProfile(t *testing.T) {
	for _, tc := range []struct {
		name    string
		profile store.ClaimProfileResolver
		want    runstate.ReconcileKind
	}{
		{name: "without host observation", want: runstate.Dispatch},
		{name: "with claim profile", profile: acceptanceClaimProfile{}, want: runstate.Execute},
	} {
		t.Run(tc.name, func(t *testing.T) {
			st := storetest.NewSQLite(t).Open(t, fixturePolicies(store.Policies{ClaimProfiles: tc.profile}))
			tenant, plan, err := storePlanSealRequest(t.Context(), st)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := tenant.AcceptPlan(t.Context(), plan); err != nil {
				t.Fatal(err)
			}
			ref := runspec.NodeRef{RunID: runspec.RunID(plan.RunID), NodeID: "alpha"}
			got, err := tenant.ReconcileNode(t.Context(), store.ReconcileRequest{Ref: ref, Actor: plan.Actor, Policy: runstate.SourceNone})
			if err != nil || got.Kind != tc.want || (got.Claim != nil) != (tc.want == runstate.Execute) {
				t.Fatalf("reconcile kind=%s claim=%+v: %v; want %s", got.Kind, got.Claim, err, tc.want)
			}
		})
	}
}
